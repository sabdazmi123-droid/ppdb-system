package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	gomysql "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ppdb-system/models"
)

// Store wraps a GORM database connection
type Store struct {
	DB       *gorm.DB
	schoolMu sync.RWMutex
	school   *models.SchoolProfile
}

type NISNImportRow struct {
	FullName string
	NISN     string
}

type NISNImportPreviewRow struct {
	FullName      string
	NIK           string
	OldNISN       string
	NewNISN       string
	Action        string
	ActionLabel   string
	StudentStatus string
}

// GlobalStore is the global store instance accessed by handlers
var GlobalStore *Store

// HashPassword hashes a plain-text password using bcrypt
func HashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("ERROR: bcrypt hash failed; refusing to store an unhashed password: %v", err)
		return "$invalid-bcrypt-hash$"
	}
	return string(hash)
}

// CheckPassword compares a bcrypt hash with a plain-text password
func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// InitStore uses the configured MySQL server, or SQLite for local development.
func InitStore(dir string) (*Store, error) {
	var dialector gorm.Dialector
	var dbLabel string
	mysqlURL := os.Getenv("MYSQL_URL")
	if mysqlURL == "" {
		mysqlURL = os.Getenv("DATABASE_URL")
	}
	if mysqlURL == "" {
		mysqlURL = os.Getenv("MYSQL_DSN")
	}
	if mysqlURL != "" {
		dsn, err := mysqlDSN(mysqlURL)
		if err != nil {
			return nil, fmt.Errorf("format koneksi MySQL tidak valid: %w", err)
		}
		dialector = gormmysql.Open(dsn)
		dbLabel = "MySQL"
	} else {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
		dbPath := filepath.Join(dir, "ppdb.db")
		dialector = sqlite.Open(dbPath + "?_journal_mode=WAL&_busy_timeout=5000")
		dbLabel = "SQLite"
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			LogLevel: logger.Warn, ParameterizedQueries: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("gagal membuka database %s: %w", dbLabel, err)
	}

	if dbLabel == "SQLite" {
		// Enable WAL mode for better concurrent performance.
		db.Exec("PRAGMA journal_mode=WAL")
		db.Exec("PRAGMA busy_timeout=5000")
		db.Exec("PRAGMA synchronous=NORMAL")
		db.Exec("PRAGMA cache_size=-20000")
		db.Exec("PRAGMA foreign_keys=ON")
	}

	// Auto-migrate all tables
	if err := db.AutoMigrate(
		&models.SchoolProfile{},
		&models.User{},
		&models.PasswordResetToken{},
		&models.PortalSettings{},
		&models.PortalItem{},
		&models.PortalRevision{},
		&models.StudentForm{},
		&models.NISNChangeLog{},
		&models.ClassDetail{},
		&models.ActivityLog{},
		&models.Notification{},
	); err != nil {
		return nil, fmt.Errorf("gagal auto-migrate: %w", err)
	}
	if dbLabel == "MySQL" {
		sqlitePath := filepath.Join(dir, "ppdb.db")
		if err := migrateSQLiteToMySQL(db, sqlitePath); err != nil {
			return nil, err
		}
	}

	s := &Store{DB: db}
	if err := s.ensurePortalDefaults(); err != nil {
		return nil, fmt.Errorf("gagal menyiapkan konten portal: %w", err)
	}

	// Seed default data if the database is empty
	var userCount int64
	db.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		if os.Getenv("APP_ENV") == "production" {
			if err := s.seedProductionAdmin(); err != nil {
				return nil, err
			}
		} else {
			s.seedDefaultData()
		}
	}
	activeSchool := s.GetSchool()
	if activeSchool.ID != 0 && !validAcademicYearFrom2026(activeSchool.CurrentYear) {
		activeSchool.CurrentYear = "2026/2027"
		if err := s.UpdateSchoolSettings(activeSchool); err != nil {
			return nil, fmt.Errorf("gagal menyelaraskan tahun ajaran aktif: %w", err)
		}
	}

	GlobalStore = s
	return s, nil
}

// migrateSQLiteToMySQL imports the existing Railway volume database on the
// first MySQL startup. Both databases must be empty/available as appropriate;
// the import runs in one transaction so a failed import can safely be retried.
func migrateSQLiteToMySQL(target *gorm.DB, sqlitePath string) error {
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("gagal memeriksa database SQLite lama: %w", err)
	}

	source, err := gorm.Open(sqlite.Open(sqlitePath+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			LogLevel: logger.Warn, ParameterizedQueries: true,
		}),
	})
	if err != nil {
		return fmt.Errorf("gagal membuka database SQLite lama untuk migrasi: %w", err)
	}
	sourceSQL, err := source.DB()
	if err != nil {
		return err
	}
	defer sourceSQL.Close()

	modelsToCopy := []interface{}{
		&models.SchoolProfile{}, &models.User{}, &models.PasswordResetToken{},
		&models.PortalSettings{}, &models.PortalItem{}, &models.PortalRevision{},
		&models.StudentForm{}, &models.NISNChangeLog{}, &models.ClassDetail{},
		&models.ActivityLog{}, &models.Notification{},
	}
	var targetRows int64
	for _, model := range modelsToCopy {
		if target.Migrator().HasTable(model) {
			var count int64
			if err := target.Model(model).Count(&count).Error; err != nil {
				return fmt.Errorf("gagal memeriksa tabel MySQL %s: %w", modelTableName(target, model), err)
			}
			targetRows += count
		}
	}
	if targetRows > 0 {
		log.Printf("MySQL sudah berisi data; migrasi otomatis dari SQLite dilewati")
		return nil
	}

	return target.Transaction(func(tx *gorm.DB) error {
		for _, model := range modelsToCopy {
			if !source.Migrator().HasTable(model) {
				continue
			}
			slicePtr := reflect.New(reflect.SliceOf(reflect.TypeOf(model).Elem())).Interface()
			if err := source.Find(slicePtr).Error; err != nil {
				return fmt.Errorf("gagal membaca tabel SQLite %s: %w", modelTableName(source, model), err)
			}
			rows := reflect.ValueOf(slicePtr).Elem()
			if rows.Len() == 0 {
				continue
			}
			normalizeZeroTimePointers(rows)
			if err := tx.CreateInBatches(slicePtr, 100).Error; err != nil {
				return fmt.Errorf("gagal menyalin tabel %s ke MySQL: %w", modelTableName(source, model), err)
			}
			log.Printf("Migrasi SQLite → MySQL: %s (%d baris)", modelTableName(source, model), rows.Len())
		}
		return nil
	})
}

func normalizeZeroTimePointers(rows reflect.Value) {
	timeType := reflect.TypeOf(time.Time{})
	for i := 0; i < rows.Len(); i++ {
		row := rows.Index(i)
		for fieldIndex := 0; fieldIndex < row.NumField(); fieldIndex++ {
			field := row.Field(fieldIndex)
			if field.Kind() == reflect.Ptr && field.Type().Elem() == timeType && !field.IsNil() && field.Elem().Interface().(time.Time).IsZero() {
				field.SetZero()
			}
		}
	}
}

func modelTableName(db *gorm.DB, model interface{}) string {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		return fmt.Sprintf("%T", model)
	}
	return statement.Schema.Table
}

// mysqlDSN accepts Railway's mysql:// URL or a native MySQL DSN.
func mysqlDSN(connection string) (string, error) {
	if !strings.Contains(connection, "://") {
		return connection, nil
	}
	u, err := url.Parse(connection)
	if err != nil {
		return "", err
	}
	if u.Scheme != "mysql" {
		return "", fmt.Errorf("skema %q bukan MySQL", u.Scheme)
	}
	password, _ := u.User.Password()
	cfg := gomysql.Config{
		User:                 u.User.Username(),
		Passwd:               password,
		Net:                  "tcp",
		Addr:                 u.Host,
		DBName:               strings.TrimPrefix(u.Path, "/"),
		ParseTime:            true,
		AllowNativePasswords: true,
	}
	return cfg.FormatDSN(), nil
}

func validAcademicYearFrom2026(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 || len(parts[0]) != 4 || len(parts[1]) != 4 {
		return false
	}
	start, errStart := strconv.Atoi(parts[0])
	end, errEnd := strconv.Atoi(parts[1])
	return errStart == nil && errEnd == nil && start >= 2026 && end == start+1
}

func (s *Store) ensurePortalDefaults() error {
	settings := models.PortalSettings{ID: 1}
	if err := s.DB.FirstOrCreate(&settings, models.PortalSettings{ID: 1}).Error; err != nil {
		return err
	}
	defaults := models.PortalSettings{
		ID: 1, HeroTitle: "Selamat Datang di RA Tunas Cendekia",
		HeroDescription: "Pendidikan usia dini yang menyenangkan, Islami, dan membangun kemandirian anak.",
		HeroImage:       "/static/img/login-bg.png",
		Announcement:    "Pendaftaran peserta didik baru tahun ajaran aktif telah dibuka.",
		AnnouncementURL: "/register", ContactText: "Hubungi Panitia PPDB",
		ContactURL: "https://wa.me/",
	}
	if err := s.DB.Model(&models.PortalSettings{}).Where("id = 1 AND hero_title = ''").Updates(defaults).Error; err != nil {
		return err
	}
	services := []models.PortalItem{
		{ID: "portal-ppdb", Kind: "service", Title: "PPDB", Subtitle: "Penerimaan Peserta Didik Baru", Body: "Mulai pendaftaran atau masuk untuk melanjutkan pengisian formulir.", Icon: "fa-solid fa-user-graduate", URL: "/register", SortOrder: 1, Active: true},
		{ID: "portal-rdm", Kind: "service", Title: "RDM (Raport Digital Madrasah)", Subtitle: "Aplikasi Raport Digital Madrasah", Icon: "fa-solid fa-book-open-reader", URL: "https://rdm.hdmadrasah.id/apps", SortOrder: 2, Active: true},
		{ID: "portal-emis", Kind: "service", Title: "EMIS", Subtitle: "Data pendidikan madrasah", Icon: "fa-solid fa-database", URL: "https://emis.kemenag.go.id", SortOrder: 3, Active: true},
		{ID: "portal-emis-gtk", Kind: "service", Title: "EMIS GTK", Subtitle: "Data guru dan tenaga kependidikan", Icon: "fa-solid fa-chalkboard-user", URL: "https://emisgtk.kemenag.go.id", SortOrder: 4, Active: true},
		{ID: "portal-pdum", Kind: "service", Title: "PDUM", Subtitle: "Pendataan peserta didik madrasah", Icon: "fa-solid fa-user-graduate", URL: "https://pdum.kemenag.go.id", SortOrder: 5, Active: true},
		{ID: "portal-bos", Kind: "service", Title: "BOS", Subtitle: "Layanan Bantuan Operasional Sekolah", Icon: "fa-solid fa-coins", URL: "https://bos.kemenag.go.id", SortOrder: 6, Active: true},
		{ID: "portal-verval-pd", Kind: "service", Title: "Verval PD", Subtitle: "Verifikasi dan validasi data peserta didik", Icon: "fa-solid fa-user-check", URL: "https://vervalpdnew2.data.kemdikbud.go.id/", SortOrder: 7, Active: true},
		{ID: "portal-verval-sp", Kind: "service", Title: "Verval SP", Subtitle: "Verifikasi dan validasi satuan pendidikan", Icon: "fa-solid fa-school-circle-check", URL: "https://vervalsp.data.kemendikdasmen.go.id", SortOrder: 8, Active: true},
	}
	for _, item := range services {
		var existing models.PortalItem
		err := s.DB.First(&existing, "id = ?", item.ID).Error
		if err == gorm.ErrRecordNotFound {
			if err := s.DB.Create(&item).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		// Migrate the old default label while preserving any title edited by an admin.
		if item.ID == "portal-ppdb" && existing.Title == "Pendaftaran PPDB" {
			if err := s.DB.Model(&existing).Updates(map[string]interface{}{"title": "PPDB", "subtitle": item.Subtitle, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
	}
	// Provide editable starter copy for the public school profile sections.
	// Stable IDs make this safe to run on every startup without overwriting CMS edits.
	starterContent := []models.PortalItem{
		{ID: "portal-info-profile", Kind: "info", Category: "profil", Title: "Mengenal RA Tunas Cendekia", Subtitle: "Tempat anak tumbuh, belajar, dan berakhlak baik", Body: "RA Tunas Cendekia mendampingi anak usia dini melalui pengalaman belajar yang menyenangkan, aman, dan sesuai tahap perkembangannya. Kami mengajak keluarga dan madrasah bekerja sama menumbuhkan rasa ingin tahu, kemandirian, serta kepedulian anak setiap hari.", Icon: "fa-solid fa-school", SortOrder: 1, Active: true},
		{ID: "portal-info-vision", Kind: "info", Category: "visi-misi", Title: "Visi Madrasah", Subtitle: "Arah dan cita-cita pendidikan", Body: "Mewujudkan generasi yang beriman, berakhlak mulia, mandiri, sehat, kreatif, dan siap belajar sepanjang hayat.", Icon: "fa-solid fa-compass", SortOrder: 1, Active: true},
		{ID: "portal-info-mission", Kind: "info", Category: "visi-misi", Title: "Misi Madrasah", Subtitle: "Langkah kami mendampingi setiap anak", Body: "Menyelenggarakan pembelajaran yang hangat dan berpusat pada anak; membiasakan nilai-nilai Islami dalam kegiatan sehari-hari; mengembangkan potensi, kreativitas, dan kemandirian; serta menjalin kemitraan yang baik dengan orang tua.", Icon: "fa-solid fa-bullseye", SortOrder: 2, Active: true},
		{ID: "portal-info-learning", Kind: "info", Category: "pembelajaran", Title: "Belajar Melalui Bermain", Subtitle: "Pengalaman belajar sesuai usia anak", Body: "Anak belajar melalui bermain, bercerita, berkarya, bergerak, dan mencoba hal baru. Guru mendampingi dengan kegiatan yang terarah namun tetap menyenangkan, sambil memperhatikan perkembangan bahasa, sosial-emosional, motorik, kognitif, serta nilai agama dan budi pekerti.", Icon: "fa-solid fa-book-open-reader", SortOrder: 1, Active: true},
	}
	for _, item := range starterContent {
		var existing models.PortalItem
		err := s.DB.First(&existing, "id = ?", item.ID).Error
		if err == gorm.ErrRecordNotFound {
			if err := s.DB.Create(&item).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// seedProductionAdmin creates only the configured operator account. It never
// copies the development demo students or default demo credentials to production.
func (s *Store) seedProductionAdmin() error {
	nik := os.Getenv("PPDB_ADMIN_NIK")
	password := os.Getenv("PPDB_ADMIN_PASSWORD")
	if nik == "" || password == "" {
		return fmt.Errorf("APP_ENV=production memerlukan PPDB_ADMIN_NIK dan PPDB_ADMIN_PASSWORD")
	}
	if len(password) < 12 || len([]byte(password)) > 72 {
		return fmt.Errorf("PPDB_ADMIN_PASSWORD harus terdiri dari 12 sampai 72 byte")
	}
	admin := models.User{
		ID: "USR-ADMIN-1", NIK: nik, FullName: "Administrator",
		NickName: "Operator", Role: "admin", PasswordHash: HashPassword(password),
		CreatedAt: time.Now(),
	}
	if err := s.DB.Create(&admin).Error; err != nil {
		return fmt.Errorf("gagal membuat akun operator produksi: %w", err)
	}
	return nil
}

// ---- Helper property-like methods for handler compatibility ----

// School returns the school profile (first record).
func (s *Store) GetSchool() models.SchoolProfile {
	s.schoolMu.RLock()
	if s.school != nil {
		cached := *s.school
		s.schoolMu.RUnlock()
		return cached
	}
	s.schoolMu.RUnlock()

	var school models.SchoolProfile
	if err := s.DB.First(&school).Error; err != nil {
		return models.SchoolProfile{}
	}
	s.schoolMu.Lock()
	if s.school == nil {
		cached := school
		s.school = &cached
	}
	cached := *s.school
	s.schoolMu.Unlock()
	return cached
}

// SetSchool updates the cached school profile
func (s *Store) SetSchool(school models.SchoolProfile) {
	s.schoolMu.Lock()
	cached := school
	s.school = &cached
	s.schoolMu.Unlock()
}

// UpdateSchoolSettings persists the current academic year and registration state.
func (s *Store) UpdateSchoolSettings(school models.SchoolProfile) error {
	if school.ID == 0 {
		school.ID = 1
	}
	if err := s.DB.Save(&school).Error; err != nil {
		return err
	}
	s.SetSchool(school)
	return nil
}

// InvalidateSchoolCache clears the school cache
func (s *Store) InvalidateSchoolCache() {
	s.schoolMu.Lock()
	s.school = nil
	s.schoolMu.Unlock()
}

// GetUserByNIK fetches a user by NIK
func (s *Store) GetUserByNIK(nik string) (models.User, bool) {
	var u models.User
	result := s.DB.Where("nik = ?", nik).First(&u)
	return u, result.Error == nil
}

// GetClassByName fetches a class by name
func (s *Store) GetClassByName(name string) (models.ClassDetail, bool) {
	var c models.ClassDetail
	result := s.DB.Where("class_name = ?", name).First(&c)
	return c, result.Error == nil
}

// GetAllClasses returns all classes in order
func (s *Store) GetAllClasses() []models.ClassDetail {
	var classes []models.ClassDetail
	s.DB.Order("class_name ASC").Find(&classes)
	return classes
}

func (s *Store) UpdateClassStaff(className, teacherName, homeroomTeacher string) error {
	className = strings.TrimSpace(className)
	if className == "" || strings.TrimSpace(teacherName) == "" || strings.TrimSpace(homeroomTeacher) == "" {
		return fmt.Errorf("nama kelas, guru pengajar, dan wali kelas wajib diisi")
	}
	class, ok := s.GetClassByName(className)
	if !ok {
		class = models.ClassDetail{ID: "CLS-" + strings.NewReplacer(" ", "", "/", "-").Replace(className), ClassName: className, AcademicYear: s.GetSchool().CurrentYear}
	}
	class.TeacherName = strings.TrimSpace(teacherName)
	class.HomeroomTeacher = strings.TrimSpace(homeroomTeacher)
	class.UpdatedAt = time.Now()
	return s.DB.Save(&class).Error
}

// GetRecentActivities returns the most recent N activities
func (s *Store) GetRecentActivities(limit int) []models.ActivityLog {
	var activities []models.ActivityLog
	s.DB.Order("created_at DESC").Limit(limit).Find(&activities)
	return activities
}

// GetNotifications returns all notifications
func (s *Store) GetNotifications() []models.Notification {
	var notifs []models.Notification
	s.DB.Order("created_at DESC").Limit(10).Find(&notifs)
	return notifs
}

// Save is a no-op for GORM compatibility (GORM auto-saves)
func (s *Store) Save() error {
	return nil
}

// ---- Core Business Logic ----

// CreatePublicRegistration stores an application before the admin creates a login account.
func (s *Store) CreatePublicRegistration(form models.StudentForm) error {
	if strings.TrimSpace(form.Field05NIK) == "" {
		return fmt.Errorf("NIK siswa wajib diisi")
	}
	if _, exists := s.GetStudentByNIK(form.Field05NIK); exists {
		return fmt.Errorf("NIK ini sudah pernah mengirim formulir")
	}
	if form.ID == "" {
		form.ID = fmt.Sprintf("STU-%d", time.Now().UnixNano())
	}
	form.UserID = ""
	// The public short form only creates a provisional record. The student's
	// complete PPDB form has not been submitted yet, so their account must start
	// in an editable draft state after the admin creates the login.
	form.PPDBStatus = "DRAFT"
	form.StudentStatus = "PENDAFTAR"
	form.StatusKeaktifan = "Calon Siswa"
	form.EmisStatus = "BELUM_INPUT"
	form.CreatedAt = time.Now()
	form.UpdatedAt = form.CreatedAt
	return s.DB.Create(&form).Error
}

// CreateStudentAccount creates a login for a submitted registration.
func (s *Store) CreateStudentAccount(nik, password string) error {
	var form models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&form).Error; err != nil {
		return fmt.Errorf("formulir siswa tidak ditemukan")
	}
	if _, exists := s.GetUserByNIK(nik); exists {
		return fmt.Errorf("akun untuk siswa ini sudah dibuat")
	}
	ciphertext, err := encryptAccountPassword(nik, password)
	if err != nil {
		return fmt.Errorf("password akun gagal diamankan untuk dokumen: %w", err)
	}
	user := models.User{
		ID: fmt.Sprintf("USR-%s", nik), NIK: nik, FullName: form.Field01FullName,
		NickName: form.ApplicantNickName, Email: form.ApplicantEmail, BirthPlace: form.Field02BirthPlace,
		BirthDate: form.Field03BirthDate, Gender: form.Field06Gender,
		MotherName: form.Field32MotherName, Phone: form.Field13PhoneWA,
		Role: "student", PasswordHash: HashPassword(password), PasswordCiphertext: ciphertext, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("gagal membuat akun siswa: %w", err)
		}
		if err := tx.Model(&form).Updates(map[string]interface{}{"user_id": user.ID, "updated_at": time.Now()}).Error; err != nil {
			return fmt.Errorf("gagal menghubungkan akun dengan formulir: %w", err)
		}
		return nil
	})
}

// GetStudentAccountPassword decrypts the password only when an operator
// explicitly requests the account document. The database stores authenticated
// ciphertext; login continues to use the bcrypt hash.
func (s *Store) GetStudentAccountPassword(nik string) (string, error) {
	account, ok := s.GetUserByNIK(nik)
	if !ok || account.Role != "student" {
		return "", fmt.Errorf("akun siswa tidak ditemukan")
	}
	if account.PasswordCiphertext == "" {
		return "", fmt.Errorf("password akun lama tidak dapat dipulihkan; atur ulang password akun terlebih dahulu")
	}
	return decryptAccountPassword(nik, account.PasswordCiphertext)
}

func accountPasswordKey() []byte {
	secret := os.Getenv("PPDB_SESSION_SECRET")
	if secret == "" && os.Getenv("APP_ENV") != "production" {
		secret = "ppdb-development-only-session-secret"
	}
	key := sha256.Sum256([]byte("ppdb-account-document-password:v1:" + secret))
	return key[:]
}

func encryptAccountPassword(nik, password string) (string, error) {
	block, err := aes.NewCipher(accountPasswordKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(password), []byte(nik))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decryptAccountPassword(nik, encoded string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("data password terenkripsi tidak valid")
	}
	block, err := aes.NewCipher(accountPasswordKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("data password terenkripsi tidak lengkap")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(nik))
	if err != nil {
		return "", fmt.Errorf("password akun tidak dapat dibuka; periksa konfigurasi secret aplikasi")
	}
	return string(plain), nil
}

// DeleteApplicantRegistration removes a pre-enrollment application and its
// generated login, while protecting students who have already become active.
func (s *Store) DeleteApplicantRegistration(nik string) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var form models.StudentForm
		if err := tx.Where("field05_nik = ?", nik).First(&form).Error; err != nil {
			return fmt.Errorf("data calon siswa tidak ditemukan")
		}
		if form.StudentStatus != "PENDAFTAR" {
			return fmt.Errorf("data sudah menjadi siswa aktif/diterima dan tidak dapat dihapus dari menu ini")
		}
		if form.UserID != "" {
			var account models.User
			if err := tx.Where("id = ? AND nik = ? AND role = ?", form.UserID, nik, "student").First(&account).Error; err != nil {
				return fmt.Errorf("akun calon siswa tidak cocok; data tidak dihapus")
			}
			if err := tx.Where("user_id = ?", account.ID).Delete(&models.PasswordResetToken{}).Error; err != nil {
				return fmt.Errorf("token pemulihan akun gagal dihapus: %w", err)
			}
			if err := tx.Delete(&account).Error; err != nil {
				return fmt.Errorf("akun calon siswa gagal dinonaktifkan: %w", err)
			}
		}
		result := tx.Where("id = ? AND student_status = ?", form.ID, "PENDAFTAR").Delete(&models.StudentForm{})
		if result.Error != nil {
			return fmt.Errorf("data calon siswa gagal dihapus: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("data calon siswa sudah berubah dan tidak dapat dihapus")
		}
		return nil
	})
}

// GetDashboardStats computes real-time statistics
func (s *Store) GetDashboardStats() models.DashboardStats {
	var total int64
	s.DB.Model(&models.StudentForm{}).Count(&total)

	var diterima, aktif, keluar, lulus int64
	s.DB.Model(&models.StudentForm{}).Where("student_status = ?", "DITERIMA").Count(&diterima)
	// Siswa yang baru disetujui masuk ke status AKTIF langsung; hitung juga
	// status DITERIMA agar data lama tetap tercakup sebagai siswa aktif.
	s.DB.Model(&models.StudentForm{}).Where("student_status IN ?", []string{"AKTIF", "DITERIMA"}).Count(&aktif)
	s.DB.Model(&models.StudentForm{}).Where("student_status = ?", "PINDAH").Count(&keluar)
	s.DB.Model(&models.StudentForm{}).Where("student_status = ?", "LULUS").Count(&lulus)

	return models.DashboardStats{
		TotalPendaftar: int(total),
		SiswaDiterima:  int(diterima),
		SiswaAktif:     int(aktif),
		SiswaKeluar:    int(keluar),
		SiswaLulus:     int(lulus),
	}
}

// GetDashboardMonthlyStats returns registration and acceptance counts by month.
func (s *Store) GetDashboardMonthlyStats(year int) ([]int, []int) {
	registered := make([]int, 12)
	accepted := make([]int, 12)
	for month := 1; month <= 12; month++ {
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
		end := start.AddDate(0, 1, 0)
		var count int64
		s.DB.Model(&models.StudentForm{}).Where("created_at >= ? AND created_at < ?", start, end).Count(&count)
		registered[month-1] = int(count)
		s.DB.Model(&models.StudentForm{}).Where("created_at >= ? AND created_at < ? AND student_status IN ?", start, end, []string{"AKTIF", "DITERIMA"}).Count(&count)
		accepted[month-1] = int(count)
	}
	return registered, accepted
}

// GetAllStudents returns all students ordered by newest first
func (s *Store) GetAllStudents() []models.StudentForm {
	var students []models.StudentForm
	s.DB.Order("created_at DESC").Find(&students)
	return students
}

func (s *Store) GetRecentNISNChanges(limit int) []models.NISNChangeLog {
	if limit <= 0 {
		limit = 20
	}
	var changes []models.NISNChangeLog
	s.DB.Order("changed_at DESC").Limit(limit).Find(&changes)
	return changes
}

// PreviewActiveStudentNISN compares imported values with current records without changing the database.
func (s *Store) PreviewActiveStudentNISN(records map[string]NISNImportRow) ([]NISNImportPreviewRow, error) {
	var preview []NISNImportPreviewRow
	for nik, row := range records {
		var existing models.StudentForm
		err := s.DB.Where("nisn = ? AND field05_nik <> ?", row.NISN, nik).First(&existing).Error
		if err == nil {
			return nil, fmt.Errorf("NISN %s sudah terdaftar pada siswa lain", row.NISN)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}

		var student models.StudentForm
		result := s.DB.Where("field05_nik = ?", nik).First(&student)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			preview = append(preview, NISNImportPreviewRow{FullName: row.FullName, NIK: nik, NewNISN: row.NISN, Action: "skip", ActionLabel: "Siswa tidak ditemukan"})
			continue
		}
		if result.Error != nil {
			return nil, result.Error
		}
		if normalizeStudentName(student.Field01FullName) != normalizeStudentName(row.FullName) {
			return nil, fmt.Errorf("nama pada file tidak cocok dengan NIK %s; pratinjau dibatalkan", nik)
		}
		entry := NISNImportPreviewRow{FullName: student.Field01FullName, NIK: nik, OldNISN: student.NISN, NewNISN: row.NISN, StudentStatus: student.StudentStatus}
		switch {
		case student.StudentStatus != "AKTIF" && student.StudentStatus != "DITERIMA":
			entry.Action, entry.ActionLabel = "skip", "Bukan siswa aktif/diterima"
		case student.NISN == row.NISN:
			entry.Action, entry.ActionLabel = "unchanged", "Tidak berubah"
		case student.NISN == "":
			entry.Action, entry.ActionLabel = "fill", "Isi NISN kosong"
		default:
			entry.Action, entry.ActionLabel = "replace", "Koreksi NISN"
		}
		preview = append(preview, entry)
	}
	sort.Slice(preview, func(i, j int) bool {
		if preview[i].FullName == preview[j].FullName {
			return preview[i].NIK < preview[j].NIK
		}
		return preview[i].FullName < preview[j].FullName
	})
	return preview, nil
}

// ApplyActiveStudentNISN applies an explicitly confirmed preview and records every change.
func (s *Store) ApplyActiveStudentNISN(records map[string]NISNImportRow, expected map[string]NISNImportPreviewRow, changedBy string) (int, int, error) {
	updated, skipped := 0, 0
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		for nik, row := range records {
			var existing models.StudentForm
			err := tx.Where("nisn = ? AND field05_nik <> ?", row.NISN, nik).First(&existing).Error
			if err == nil {
				return fmt.Errorf("NISN %s sudah terdaftar pada siswa lain", row.NISN)
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		for nik, row := range records {
			preview, hasPreview := expected[nik]
			if !hasPreview {
				return fmt.Errorf("pratinjau impor tidak lengkap; unggah ulang file")
			}
			if preview.Action == "skip" || preview.Action == "unchanged" {
				skipped++
				continue
			}
			var student models.StudentForm
			result := tx.Where("field05_nik = ?", nik).First(&student)
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				skipped++
				continue
			}
			if result.Error != nil {
				return result.Error
			}
			if student.NISN != preview.OldNISN || student.StudentStatus != preview.StudentStatus || normalizeStudentName(student.Field01FullName) != normalizeStudentName(preview.FullName) {
				return fmt.Errorf("data %s berubah setelah pratinjau; unggah ulang file untuk meninjau perubahan terbaru", preview.FullName)
			}
			if normalizeStudentName(student.Field01FullName) != normalizeStudentName(row.FullName) {
				return fmt.Errorf("nama pada file tidak cocok dengan NIK %s; impor dibatalkan", nik)
			}
			if student.NISN == row.NISN {
				skipped++
				continue
			}
			oldNISN := student.NISN
			if err := tx.Model(&student).Updates(map[string]interface{}{"nisn": row.NISN, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
			if err := tx.Create(&models.NISNChangeLog{
				ID: fmt.Sprintf("NISN-%d-%s", time.Now().UnixNano(), nik), StudentNIK: nik,
				StudentName: student.Field01FullName, OldNISN: oldNISN, NewNISN: row.NISN,
				ChangedBy: changedBy, ChangedAt: time.Now(),
			}).Error; err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, skipped, err
}

func normalizeStudentName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

// GetStudentByNIK fetches a student by NIK
func (s *Store) GetStudentByNIK(nik string) (models.StudentForm, bool) {
	var st models.StudentForm
	result := s.DB.Where("field05_nik = ?", nik).First(&st)
	return st, result.Error == nil
}

// UpdateStudentForm saves changes to a student form
func (s *Store) UpdateStudentForm(nik string, f models.StudentForm) error {
	var existing models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&existing).Error; err != nil {
		return fmt.Errorf("data siswa tidak ditemukan")
	}

	f.ID = existing.ID
	f.UserID = existing.UserID
	f.Field05NIK = nik
	f.CreatedAt = existing.CreatedAt
	f.UpdatedAt = time.Now()

	if f.NISLokal == "" {
		f.NISLokal = existing.NISLokal
	}
	if f.NISN == "" {
		f.NISN = existing.NISN
	}

	return s.DB.Save(&f).Error
}

// ApproveFormulirPPDB approves a form -> issues NIS Lokal -> student accepted
func (s *Store) ApproveFormulirPPDB(nik string, assignedClass string, admissionDate string) (models.StudentForm, error) {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return st, fmt.Errorf("siswa tidak ditemukan")
	}

	school := s.GetSchool()

	// Generate NIS Lokal
	if st.NISLokal == "" {
		nsm := school.NSM
		if nsm == "" {
			nsm = "101232010088"
		}
		year := time.Now().Format("2006")
		if len(st.AcademicYear) >= 4 {
			year = st.AcademicYear[:4]
		}
		prefix := nsm + year

		// Find max sequence
		var maxNIS string
		s.DB.Model(&models.StudentForm{}).
			Where("nis_lokal LIKE ?", prefix+"%").
			Order("nis_lokal DESC").
			Limit(1).
			Pluck("nis_lokal", &maxNIS)

		maxSeq := 0
		if maxNIS != "" && len(maxNIS) == len(prefix)+4 {
			if seq, err := strconv.Atoi(maxNIS[len(prefix):]); err == nil {
				maxSeq = seq
			}
		}
		st.NISLokal = fmt.Sprintf("%s%04d", prefix, maxSeq+1)
	}

	st.PPDBStatus = "DISETUJUI"
	st.RevisionNotes = ""
	st.StudentStatus = "AKTIF"
	st.StatusKeaktifan = "Siswa Aktif"
	st.CurrentClass = assignedClass
	st.AdmissionDate = admissionDate
	st.EmisStatus = "BELUM_INPUT"
	st.UpdatedAt = time.Now()

	s.DB.Save(&st)
	s.addActivity("Siswa disetujui & aktif",
		fmt.Sprintf("%s (%s) - Siswa Aktif (NIS: %s)", st.Field01FullName, st.CurrentClass, st.NISLokal),
		"accept")

	return st, nil
}

// RejectFormulirPPDB rejects a form with revision notes
func (s *Store) RejectFormulirPPDB(nik string, notes string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	updates := map[string]interface{}{
		"revision_notes": notes,
		"updated_at":     time.Now(),
	}
	activeOrApproved := st.PPDBStatus == "DISETUJUI" || st.StudentStatus == "AKTIF" || st.StudentStatus == "DITERIMA"
	if activeOrApproved {
		// Keep approval, NIS, and class intact; grant the student one controlled edit.
		updates["profile_edit_allowed"] = true
		updates["profile_edit_notes"] = notes
	} else {
		updates["ppdb_status"] = "PERLU_PERBAIKAN"
	}
	if err := s.DB.Model(&st).Updates(updates).Error; err != nil {
		return fmt.Errorf("gagal membuka formulir untuk perbaikan: %w", err)
	}

	action := "Formulir perlu perbaikan"
	if activeOrApproved {
		action = "Formulir siswa aktif dibuka untuk perbaikan"
	}
	s.addActivity(action, fmt.Sprintf("%s: %s", st.Field01FullName, notes), "form")
	return nil
}

// UpdateEmisStatus updates EMIS status
func (s *Store) UpdateEmisStatus(nik, status, notes string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	updates := map[string]interface{}{
		"emis_status": status,
		"emis_notes":  notes,
		"updated_at":  time.Now(),
	}
	if status == "APPROVED_EMIS" {
		updates["student_status"] = "AKTIF"
		updates["status_keaktifan"] = "Aktif"
	}

	return s.DB.Model(&st).Updates(updates).Error
}

// ProcessClassPromotion promotes a student to a new class
func (s *Store) ProcessClassPromotion(nik, targetClass, academicYear string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	updates := map[string]interface{}{
		"current_class":    targetClass,
		"student_status":   "AKTIF",
		"status_keaktifan": "Aktif",
		"updated_at":       time.Now(),
	}
	if academicYear != "" {
		updates["academic_year"] = academicYear
	}

	s.DB.Model(&st).Updates(updates)
	s.addActivity("Kenaikan Kelas",
		fmt.Sprintf("%s naik ke %s (%s)", st.Field01FullName, targetClass, academicYear),
		"accept")
	return nil
}

// ProcessRepeatClass holds a student back in the same class
func (s *Store) ProcessRepeatClass(nik, targetClass string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	s.DB.Model(&st).Updates(map[string]interface{}{
		"current_class":    targetClass,
		"student_status":   "AKTIF",
		"status_keaktifan": "Aktif (Mengulang)",
		"updated_at":       time.Now(),
	})

	s.addActivity("Mengulang Kelas",
		fmt.Sprintf("%s mengulang di %s", st.Field01FullName, targetClass),
		"accept")
	return nil
}

// ProcessTransferOut transfers a student out
func (s *Store) ProcessTransferOut(nik, targetSchool, reason, outDate string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	if outDate == "" {
		outDate = time.Now().Format("2006-01-02")
	}

	s.DB.Model(&st).Updates(map[string]interface{}{
		"student_status":    "PINDAH",
		"status_keaktifan":  "Keluar",
		"out_school_target": targetSchool,
		"out_reason":        reason,
		"out_date":          outDate,
		"updated_at":        time.Now(),
	})

	s.addActivity("Siswa keluar",
		fmt.Sprintf("%s pindah ke %s", st.Field01FullName, targetSchool),
		"out")
	return nil
}

// ProcessGraduation graduates a student
func (s *Store) ProcessGraduation(nik, certNo, scanURL, gradYear string) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	school := s.GetSchool()
	if gradYear == "" {
		gradYear = school.CurrentYear
	}

	updates := map[string]interface{}{
		"student_status":     "LULUS",
		"status_keaktifan":   "Lulus",
		"certificate_number": certNo,
		"graduation_year":    gradYear,
		"updated_at":         time.Now(),
	}
	if scanURL != "" {
		updates["certificate_scan_url"] = scanURL
	}

	s.DB.Model(&st).Updates(updates)
	s.addActivity("Siswa lulus",
		fmt.Sprintf("%s dinyatakan lulus (Ijazah: %s)", st.Field01FullName, certNo),
		"grad")
	return nil
}

// ImportPastGraduate adds an alumni record
func (s *Store) ImportPastGraduate(st models.StudentForm) error {
	if st.Field05NIK == "" {
		return fmt.Errorf("NIK wajib diisi")
	}
	st.StudentStatus = "LULUS"
	st.StatusKeaktifan = "Lulus"
	st.PPDBStatus = "DISETUJUI"
	now := time.Now()
	st.CreatedAt = now
	st.UpdatedAt = now
	if st.ID == "" {
		st.ID = fmt.Sprintf("STU-%d", time.Now().UnixNano())
	}
	return s.DB.Create(&st).Error
}

// GetDefaultRDMGrades returns default grade template
func GetDefaultRDMGrades() []models.GradeItem {
	return []models.GradeItem{
		{SubjectName: "Nilai Agama dan Budi Pekerti (Al-Qur'an, Hadis, Fikih)", Score: 88, Predicate: "A", Description: "Anak sangat baik dalam melafalkan surat-surat pendek, doa harian, dan mengenal adab islami sehari-hari."},
		{SubjectName: "Jati Diri (Sosial Emosional & Karakter Anak)", Score: 86, Predicate: "A", Description: "Mampu berinteraksi secara percaya diri, berbagi dengan teman sekelas, dan mengelola emosi dengan baik."},
		{SubjectName: "Dasar-Dasar Literasi & Bahasa", Score: 85, Predicate: "A", Description: "Mampu menyimak cerita dengan antusias, menceritakan kembali ide sederhana, dan mengenal simbol huruf dasar."},
		{SubjectName: "Sains, Teknologi, Rekayasa, Seni & Matematika (STEAM)", Score: 84, Predicate: "B", Description: "Aktif mengeksplorasi lingkungan belajar, mengelompokkan bentuk dan pola warna, serta berkreasi seni secara mandiri."},
		{SubjectName: "Kosa Kata Bahasa Arab Dasar", Score: 82, Predicate: "B", Description: "Mampu menyebutkan nama-nama anggota tubuh dan warna dalam bahasa Arab dengan pengucapan yang baik."},
	}
}

// SaveRDMGrades saves grade data for a student
func (s *Store) SaveRDMGrades(nik string, grades []models.GradeItem) error {
	var st models.StudentForm
	if err := s.DB.Where("field05_nik = ?", nik).First(&st).Error; err != nil {
		return fmt.Errorf("siswa tidak ditemukan")
	}

	st.RDMGrades = models.GradeItems(grades)
	st.RDMSynced = true
	st.UpdatedAt = time.Now()
	s.DB.Save(&st)

	s.addActivity("Nilai RDM diinput/diperbarui",
		fmt.Sprintf("Rapor Digital: %s (%s)", st.Field01FullName, st.CurrentClass),
		"form")
	return nil
}

// ---- Internal helpers ----

func (s *Store) addActivity(action, detail, category string) {
	act := models.ActivityLog{
		ID:        fmt.Sprintf("ACT-%d", time.Now().UnixNano()),
		Action:    action,
		Detail:    detail,
		Category:  category,
		CreatedAt: time.Now(),
	}
	s.DB.Create(&act)
}

// ---- Seed Default Data ----

func (s *Store) seedDefaultData() {
	now := time.Now()
	school := s.GetSchool()

	// School Profile
	school = models.SchoolProfile{
		ID:               1,
		NPSN:             "69981234",
		NSM:              "101232010088",
		SchoolName:       "RA Tunas Cendekia",
		Level:            "RA",
		Address:          "Jl. Pendidikan No. 45, Kompleks Madrasah Cendekia",
		City:             "Kota Bandung",
		Province:         "Jawa Barat",
		PostalCode:       "40123",
		Phone:            "022-7890123",
		Email:            "info@tunascendekia.sch.id",
		PrincipalName:    "Dr. H. Ahmad Farhan, M.Pd.I",
		PrincipalNIP:     "197805122003121002",
		CurrentYear:      "2026/2027",
		RegistrationOpen: true,
	}
	s.DB.Create(&school)
	s.SetSchool(school)

	// Admin
	admin := models.User{
		ID:           "USR-ADMIN-1",
		NIK:          "admin",
		FullName:     "Admin Operator Madrasah",
		NickName:     "Operator",
		Email:        "operator@madrasah.sch.id",
		Phone:        "081234567890",
		Role:         "admin",
		PasswordHash: HashPassword("admin123"),
		CreatedAt:    now.Add(-60 * 24 * time.Hour),
	}
	s.DB.Create(&admin)

	// Classes
	classes := []models.ClassDetail{
		{ID: "CLS-A", ClassName: "Kelas A", AcademicYear: "2026/2027", TeacherName: "Ustadzah Siti Aminah, S.Pd", HomeroomTeacher: "Ustadzah Siti Aminah, S.Pd", Capacity: 30},
		{ID: "CLS-B1", ClassName: "Kelas B1", AcademicYear: "2026/2027", TeacherName: "Ustadz Muhammad Rizky, S.Pd.I", HomeroomTeacher: "Ustadz Muhammad Rizky, S.Pd.I", Capacity: 30},
		{ID: "CLS-B2", ClassName: "Kelas B2", AcademicYear: "2026/2027", TeacherName: "Ustadzah Nurul Hidayah, S.Pd", HomeroomTeacher: "Ustadzah Nurul Hidayah, S.Pd", Capacity: 30},
	}
	for _, c := range classes {
		s.DB.Create(&c)
	}

	// Student 1: Menunggu Verifikasi
	s1 := models.StudentForm{
		ID: "STU-001", UserID: "USR-3201010001", AcademicYear: "2026/2027",
		Field01FullName: "Ahmad Fauzi", Field02BirthPlace: "Bandung", Field03BirthDate: "2019-05-14",
		Field04Nationality: "Indonesia", Field05NIK: "3201011405190001", Field06Gender: "Laki-laki",
		Field07Religion: "Islam", Field08ChildOrder: 1, Field09SiblingsCount: 2,
		Field10Distance: "1.5 km", Field11Transport: "Sepeda Motor", Field12TravelTimeMin: 15,
		Field13PhoneWA: "081298765432", Field14NoKK: "3201011234567890", Field15FamilyHead: "Bambang Fauzi",
		Field16FatherName: "Bambang Fauzi", Field17FatherStatus: "Masih Hidup", Field18FatherNIK: "3201011005800001",
		Field22FatherJob: "Karyawan Swasta", Field23FatherIncome: "Rp 5.000.000 - Rp 10.000.000",
		Field26Province: "Jawa Barat", Field27City: "Bandung", Field30StreetAddress: "Jl. Melati No. 12 RT 02 RW 05",
		Field32MotherName: "Nurhaliza", Field33MotherStatus: "Masih Hidup", Field34MotherNIK: "3201011206820002",
		Field38MotherJob: "Ibu Rumah Tangga", Field40AttendedPAUD: "Ya", Field41AttendedTKRA: "Tidak",
		Field42ImmuneHepatitis: "Lengkap", Field43ImmunePolio: "Lengkap", Field44ImmuneBCG: "Lengkap",
		Field45ImmuneCampak: "Lengkap", Field46ImmuneDPT: "Lengkap", Field50IsConfirmed: true,
		PPDBStatus: "MENUNGGU_VERIFIKASI", EmisStatus: "BELUM_INPUT", StudentStatus: "PENDAFTAR",
		StatusKeaktifan: "Calon Siswa", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}
	s.DB.Create(&s1)
	s.DB.Create(&models.User{
		ID: "USR-3201010001", NIK: "3201011405190001", FullName: "Ahmad Fauzi", NickName: "Fauzi",
		BirthPlace: "Bandung", BirthDate: "2019-05-14", Gender: "Laki-laki", MotherName: "Nurhaliza",
		Email: "fauzi@example.com", Phone: "081298765432", Role: "student",
		PasswordHash: HashPassword("123456"), CreatedAt: now.Add(-2 * time.Hour),
	})

	// Student 2: Perlu Perbaikan
	s2 := models.StudentForm{
		ID: "STU-002", UserID: "USR-3201010002", AcademicYear: "2026/2027",
		Field01FullName: "Rizky Maulana", Field02BirthPlace: "Bandung", Field03BirthDate: "2019-08-20",
		Field04Nationality: "Indonesia", Field05NIK: "3201012008190002", Field06Gender: "Laki-laki",
		Field07Religion: "Islam", Field08ChildOrder: 2, Field09SiblingsCount: 1,
		Field13PhoneWA: "081345678901", Field14NoKK: "3201019876543210",
		Field16FatherName: "Hendra Maulana", Field32MotherName: "Rina Marlina", Field50IsConfirmed: true,
		PPDBStatus: "PERLU_PERBAIKAN", RevisionNotes: "Lampiran scan KK belum terbaca jelas dan NIK Ibu belum 16 digit. Mohon perbaiki data identitas keluarga.",
		EmisStatus: "BELUM_INPUT", StudentStatus: "PENDAFTAR", StatusKeaktifan: "Calon Siswa",
		CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
	}
	s.DB.Create(&s2)
	s.DB.Create(&models.User{
		ID: "USR-3201010002", NIK: "3201012008190002", FullName: "Rizky Maulana", NickName: "Rizky",
		MotherName: "Rina Marlina", Email: "rizky@example.com", Phone: "081345678901", Role: "student",
		PasswordHash: HashPassword("123456"), CreatedAt: now.Add(-5 * time.Hour),
	})

	// Student 3: Diterima + EMIS Approved
	s3 := models.StudentForm{
		ID: "STU-003", UserID: "USR-3201010003", AcademicYear: "2026/2027",
		Field01FullName: "Siti Nurhaliza", Field02BirthPlace: "Bandung", Field03BirthDate: "2019-03-10",
		Field04Nationality: "Indonesia", Field05NIK: "3201011003190003", Field06Gender: "Perempuan",
		Field07Religion: "Islam", Field13PhoneWA: "081567890123",
		Field16FatherName: "Sulaiman", Field32MotherName: "Halimah", Field50IsConfirmed: true,
		PPDBStatus: "DISETUJUI", NISLokal: "10123201008820250001", NISN: "3198765431",
		CurrentClass: "Kelas A", EmisStatus: "APPROVED_EMIS", StudentStatus: "DITERIMA",
		StatusKeaktifan: "Diterima", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour),
	}
	s.DB.Create(&s3)

	// Student 4: Aktif Kelas A
	s4 := models.StudentForm{
		ID: "STU-004", UserID: "USR-3201010004", AcademicYear: "2026/2027",
		Field01FullName: "Muhammad Zaki Ramadhan", Field02BirthPlace: "Bandung", Field03BirthDate: "2018-11-05",
		Field05NIK: "3201010511180004", Field06Gender: "Laki-laki", Field07Religion: "Islam",
		Field13PhoneWA: "081678901234", Field16FatherName: "H. Ridwan Ramadhan", Field32MotherName: "Hj. Aisyah",
		Field50IsConfirmed: true, PPDBStatus: "DISETUJUI", NISLokal: "10123201008820250002", NISN: "3187654321",
		CurrentClass: "Kelas A", EmisStatus: "APPROVED_EMIS", StudentStatus: "AKTIF", StatusKeaktifan: "Aktif",
		RDMSynced: true, CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now.Add(-10 * 24 * time.Hour),
	}
	s.DB.Create(&s4)

	// Student 5: Aktif Kelas B1
	s5 := models.StudentForm{
		ID: "STU-005", UserID: "USR-3201010005", AcademicYear: "2026/2027",
		Field01FullName: "Fatimah Azzahra", Field02BirthPlace: "Bandung", Field03BirthDate: "2018-07-22",
		Field05NIK: "3201012207180005", Field06Gender: "Perempuan", Field07Religion: "Islam",
		Field13PhoneWA: "081789012345", Field16FatherName: "Dedi Kurnia", Field32MotherName: "Dewi Lestari",
		Field50IsConfirmed: true, PPDBStatus: "DISETUJUI", NISLokal: "10123201008820250003", NISN: "3185544332",
		CurrentClass: "Kelas B1", EmisStatus: "APPROVED_EMIS", StudentStatus: "AKTIF", StatusKeaktifan: "Aktif",
		RDMSynced: true, CreatedAt: now.Add(-40 * 24 * time.Hour), UpdatedAt: now.Add(-12 * 24 * time.Hour),
	}
	s.DB.Create(&s5)

	// Student 6: Keluar / Pindah
	s6 := models.StudentForm{
		ID: "STU-006", UserID: "USR-3201010006", AcademicYear: "2026/2027",
		Field01FullName: "Dewi Lestari", Field02BirthPlace: "Jakarta", Field03BirthDate: "2018-09-15",
		Field05NIK: "3201011509180006", Field06Gender: "Perempuan", Field07Religion: "Islam",
		Field13PhoneWA: "081890123456", Field16FatherName: "Gunawan", Field32MotherName: "Sutini",
		Field50IsConfirmed: true, PPDBStatus: "DISETUJUI", NISLokal: "10123201008820250004", NISN: "3184433221",
		CurrentClass: "Kelas B1", StudentStatus: "PINDAH", StatusKeaktifan: "Keluar",
		OutSchoolTarget: "SMP Negeri 1 Surabaya", OutReason: "Mengikuti orang tua pindah tugas kedinasan ke Surabaya",
		OutDate: "2025-09-15", CreatedAt: now.Add(-50 * 24 * time.Hour), UpdatedAt: now.Add(-2 * 24 * time.Hour),
	}
	s.DB.Create(&s6)

	// Student 7: Lulus
	s7 := models.StudentForm{
		ID: "STU-007", UserID: "USR-3201010007", AcademicYear: "2026/2027",
		Field01FullName: "Ahmad Rifai", Field02BirthPlace: "Bandung", Field03BirthDate: "2017-04-10",
		Field05NIK: "3201011004170007", Field06Gender: "Laki-laki", Field07Religion: "Islam",
		Field13PhoneWA: "081901234567", Field16FatherName: "Rifai Santoso", Field32MotherName: "Mariam",
		Field50IsConfirmed: true, PPDBStatus: "DISETUJUI", NISLokal: "10123201008820240015", NISN: "3173322110",
		CurrentClass: "Kelas B2", StudentStatus: "LULUS", StatusKeaktifan: "Lulus",
		GraduationYear: "2024/2025", CertificateNumber: "DN-02/RA/25/0009871",
		CertificateScanURL: "/static/uploads/sample-ijazah.pdf",
		CreatedAt:          now.Add(-300 * 24 * time.Hour), UpdatedAt: now.Add(-60 * 24 * time.Hour),
	}
	s.DB.Create(&s7)

	// Activities
	activities := []models.ActivityLog{
		{ID: "ACT-1", Action: "Siswa baru mendaftar", Detail: "Ahmad Fauzi (RA)", Category: "register", CreatedAt: now.Add(-39 * time.Minute)},
		{ID: "ACT-2", Action: "Siswa diterima", Detail: "Siti Nurhaliza (RA)", Category: "accept", CreatedAt: now.Add(-52 * time.Minute)},
		{ID: "ACT-3", Action: "Formulir PPDB diisi", Detail: "Rizky Maulana (RA)", Category: "form", CreatedAt: now.Add(-64 * time.Minute)},
		{ID: "ACT-4", Action: "Siswa keluar", Detail: "Dewi Lestari (RA)", Category: "out", CreatedAt: now.Add(-94 * time.Minute)},
		{ID: "ACT-5", Action: "Siswa lulus", Detail: "Ahmad Rifai (RA)", Category: "grad", CreatedAt: now.Add(-114 * time.Minute)},
	}
	for _, a := range activities {
		s.DB.Create(&a)
	}

	// Notifications
	notifs := []models.Notification{
		{ID: "NOT-1", Title: "Pengisian data NPSN belum lengkap", Message: "Silakan lengkapi data NPSN untuk melanjutkan proses.", Type: "warning", CreatedAt: now.Add(-6 * time.Hour)},
		{ID: "NOT-2", Title: "Terdapat 12 siswa yang belum diverifikasi", Message: "Lakukan verifikasi pada menu Siswa Diterima.", Type: "success", CreatedAt: now.Add(-22 * time.Hour)},
		{ID: "NOT-3", Title: "Kenaikan kelas dan kelulusan siap diproses", Message: "Pastikan data siswa aktif sudah sesuai.", Type: "info", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "NOT-5", Title: "Pengisian NPSN telah disimpan", Message: "Data NPSN sudah tersimpan dengan baik.", Type: "info", CreatedAt: now.Add(-96 * time.Hour)},
	}
	for _, n := range notifs {
		s.DB.Create(&n)
	}

	log.Println("✅ Database SQLite berhasil diinisialisasi dengan data default")
}
