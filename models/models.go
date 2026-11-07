package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// SchoolProfile data madrasah/sekolah
type SchoolProfile struct {
	ID                  uint      `gorm:"primaryKey" json:"-"`
	NPSN                string    `gorm:"type:varchar(20)" json:"npsn"`
	NSM                 string    `gorm:"type:varchar(30)" json:"nsm"`
	SchoolName          string    `gorm:"type:varchar(200)" json:"school_name"`
	Level               string    `gorm:"type:varchar(20)" json:"level"`
	Address             string    `gorm:"type:text" json:"address"`
	City                string    `gorm:"type:varchar(100)" json:"city"`
	Province            string    `gorm:"type:varchar(100)" json:"province"`
	PostalCode          string    `gorm:"type:varchar(10)" json:"postal_code"`
	Phone               string    `gorm:"type:varchar(20)" json:"phone"`
	Email               string    `gorm:"type:varchar(100)" json:"email"`
	PrincipalName       string    `gorm:"type:varchar(200)" json:"principal_name"`
	PrincipalNIP        string    `gorm:"type:varchar(30)" json:"principal_nip"`
	LogoURL             string    `gorm:"type:text" json:"logo_url"`
	CurrentYear         string    `gorm:"type:varchar(20)" json:"current_year"`
	CustomAcademicYears string    `gorm:"type:text" json:"custom_academic_years,omitempty"`
	RegistrationOpen    bool      `gorm:"not null;default:true" json:"registration_open"`
	AdmissionQuota      int       `gorm:"not null;default:0" json:"admission_quota"`
	CreatedAt           time.Time `json:"-"`
	UpdatedAt           time.Time `json:"-"`
}

// User akun pendaftar atau admin
type User struct {
	ID                 string    `gorm:"primaryKey;type:varchar(50)" json:"id"`
	NIK                string    `gorm:"uniqueIndex;type:varchar(20)" json:"nik"`
	FullName           string    `gorm:"type:varchar(200)" json:"full_name"`
	NickName           string    `gorm:"type:varchar(100)" json:"nick_name"`
	BirthPlace         string    `gorm:"type:varchar(100)" json:"birth_place"`
	BirthDate          string    `gorm:"type:varchar(20)" json:"birth_date"`
	Gender             string    `gorm:"type:varchar(20)" json:"gender"`
	MotherName         string    `gorm:"type:varchar(200)" json:"mother_name"`
	Email              string    `gorm:"type:varchar(100)" json:"email"`
	Phone              string    `gorm:"type:varchar(20)" json:"phone"`
	Role               string    `gorm:"type:varchar(20);index" json:"role"`
	PasswordHash       string    `gorm:"type:varchar(200)" json:"password_hash"`
	PasswordCiphertext string    `gorm:"type:text" json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"-"`
}

// PasswordResetToken stores only a hash of a single-use password reset token.
type PasswordResetToken struct {
	TokenHash string    `gorm:"primaryKey;type:char(64)" json:"-"`
	UserID    string    `gorm:"type:varchar(50);index;not null" json:"-"`
	ExpiresAt time.Time `gorm:"index;not null" json:"-"`
	CreatedAt time.Time `json:"-"`
}

// PortalSettings contains public landing-page content managed by operators.
type PortalSettings struct {
	ID              uint      `gorm:"primaryKey" json:"-"`
	HeroTitle       string    `gorm:"type:varchar(240)" json:"hero_title"`
	HeroDescription string    `gorm:"type:text" json:"hero_description"`
	HeroImage       string    `gorm:"type:text" json:"hero_image"`
	Announcement    string    `gorm:"type:text" json:"announcement"`
	AnnouncementURL string    `gorm:"type:text" json:"announcement_url"`
	ContactText     string    `gorm:"type:varchar(240)" json:"contact_text"`
	ContactURL      string    `gorm:"type:text" json:"contact_url"`
	DocumentHeader  string    `gorm:"type:varchar(240)" json:"document_header"`
	FormPDFTitle    string    `gorm:"type:varchar(240)" json:"form_pdf_title"`
	AccountPDFTitle string    `gorm:"type:varchar(240)" json:"account_pdf_title"`
	DocumentFooter  string    `gorm:"type:varchar(500)" json:"document_footer"`
	DocumentStyle   string    `gorm:"type:varchar(20)" json:"document_style"`
	UpdatedAt       time.Time `json:"-"`
}

// PortalItem is a CMS entry shown in the public portal (service, info, media, news).
type PortalItem struct {
	ID          string     `gorm:"primaryKey;type:varchar(50)" json:"id"`
	Kind        string     `gorm:"type:varchar(20);index;not null" json:"kind"`
	Title       string     `gorm:"type:varchar(200);not null" json:"title"`
	Subtitle    string     `gorm:"type:varchar(240)" json:"subtitle"`
	Body        string     `gorm:"type:text" json:"body"`
	Icon        string     `gorm:"type:varchar(80)" json:"icon"`
	ImageURL    string     `gorm:"type:text" json:"image_url"`
	URL         string     `gorm:"type:text" json:"url"`
	Category    string     `gorm:"type:varchar(80)" json:"category"`
	SortOrder   int        `gorm:"index" json:"sort_order"`
	Active      bool       `gorm:"not null;default:true;index" json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at"`
}

// PortalRevision keeps an audit trail of CMS edits and removals.
type PortalRevision struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	EntityType string    `gorm:"type:varchar(30);index;not null" json:"entity_type"`
	EntityID   string    `gorm:"type:varchar(50);index;not null" json:"entity_id"`
	Title      string    `gorm:"type:varchar(240)" json:"title"`
	Action     string    `gorm:"type:varchar(20);index;not null" json:"action"`
	Actor      string    `gorm:"type:varchar(200)" json:"actor"`
	Snapshot   string    `gorm:"type:text" json:"snapshot"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// GradeItem mewakili nilai mata pelajaran atau capaian perkembangan RDM
type GradeItem struct {
	SubjectName string `json:"subject_name"`
	Score       int    `json:"score"`
	Predicate   string `json:"predicate"`
	Description string `json:"description"`
}

// GradeItems custom type untuk serialisasi JSON ke SQLite TEXT column
type GradeItems []GradeItem

func (g GradeItems) Value() (driver.Value, error) {
	if g == nil {
		return "[]", nil
	}
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (g *GradeItems) Scan(value interface{}) error {
	if value == nil {
		*g = GradeItems{}
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case string:
		bytes = []byte(v)
	case []byte:
		bytes = v
	default:
		return fmt.Errorf("cannot scan type %T into GradeItems", value)
	}
	return json.Unmarshal(bytes, g)
}

// StudentForm 50 Field Pendaftaran Lengkap + Metadata PPDB & Akademik
type StudentForm struct {
	ID                string `gorm:"primaryKey;type:varchar(50)" json:"id"`
	UserID            string `gorm:"type:varchar(50);index" json:"user_id"`
	AcademicYear      string `gorm:"type:varchar(20)" json:"academic_year"`
	AdmissionDate     string `gorm:"type:varchar(20)" json:"admission_date,omitempty"`
	ApplicantNickName string `gorm:"type:varchar(100)" json:"applicant_nick_name"`
	ApplicantEmail    string `gorm:"type:varchar(100)" json:"applicant_email"`

	// 50 Field Pendaftaran Formulir Kemenag
	Field01FullName        string `gorm:"type:varchar(200)" json:"f01_full_name"`
	Field02BirthPlace      string `gorm:"type:varchar(100)" json:"f02_birth_place"`
	Field03BirthDate       string `gorm:"type:varchar(20)" json:"f03_birth_date"`
	Field04Nationality     string `gorm:"type:varchar(50)" json:"f04_nationality"`
	Field05NIK             string `gorm:"uniqueIndex;type:varchar(20)" json:"f05_nik"`
	Field06Gender          string `gorm:"type:varchar(20)" json:"f06_gender"`
	Field07Religion        string `gorm:"type:varchar(20)" json:"f07_religion"`
	Field08ChildOrder      int    `json:"f08_child_order"`
	Field09SiblingsCount   int    `json:"f09_siblings_count"`
	Field10Distance        string `gorm:"type:varchar(50)" json:"f10_distance"`
	Field11Transport       string `gorm:"type:varchar(100)" json:"f11_transport"`
	Field12TravelTimeMin   int    `json:"f12_travel_time_min"`
	Field13PhoneWA         string `gorm:"type:varchar(20)" json:"f13_phone_wa"`
	Field14NoKK            string `gorm:"type:varchar(20)" json:"f14_no_kk"`
	Field15FamilyHead      string `gorm:"type:varchar(200)" json:"f15_family_head"`
	Field16FatherName      string `gorm:"type:varchar(200)" json:"f16_father_name"`
	Field17FatherStatus    string `gorm:"type:varchar(50)" json:"f17_father_status"`
	Field18FatherNIK       string `gorm:"type:varchar(20)" json:"f18_father_nik"`
	Field19FatherBirthPl   string `gorm:"type:varchar(100)" json:"f19_father_birth_pl"`
	Field20FatherBirthYr   string `gorm:"type:varchar(10)" json:"f20_father_birth_yr"`
	Field21FatherEdu       string `gorm:"type:varchar(50)" json:"f21_father_edu"`
	Field22FatherJob       string `gorm:"type:varchar(100)" json:"f22_father_job"`
	Field23FatherIncome    string `gorm:"type:varchar(100)" json:"f23_father_income"`
	Field24ResidenceType   string `gorm:"type:varchar(50)" json:"f24_residence_type"`
	Field25HouseOwnership  string `gorm:"type:varchar(50)" json:"f25_house_ownership"`
	Field26Province        string `gorm:"type:varchar(100)" json:"f26_province"`
	Field27City            string `gorm:"type:varchar(100)" json:"f27_city"`
	Field28District        string `gorm:"type:varchar(100)" json:"f28_district"`
	Field29Village         string `gorm:"type:varchar(100)" json:"f29_village"`
	Field30StreetAddress   string `gorm:"type:text" json:"f30_street_address"`
	Field31PostalCode      string `gorm:"type:varchar(10)" json:"f31_postal_code"`
	Field32MotherName      string `gorm:"type:varchar(200)" json:"f32_mother_name"`
	Field33MotherStatus    string `gorm:"type:varchar(50)" json:"f33_mother_status"`
	Field34MotherNIK       string `gorm:"type:varchar(20)" json:"f34_mother_nik"`
	Field35MotherBirthPl   string `gorm:"type:varchar(100)" json:"f35_mother_birth_pl"`
	Field36MotherBirthYr   string `gorm:"type:varchar(10)" json:"f36_mother_birth_yr"`
	Field37MotherEdu       string `gorm:"type:varchar(50)" json:"f37_mother_edu"`
	Field38MotherJob       string `gorm:"type:varchar(100)" json:"f38_mother_job"`
	Field39MotherIncome    string `gorm:"type:varchar(100)" json:"f39_mother_income"`
	Field40AttendedPAUD    string `gorm:"type:varchar(10)" json:"f40_attended_paud"`
	Field41AttendedTKRA    string `gorm:"type:varchar(10)" json:"f41_attended_tkra"`
	Field42ImmuneHepatitis string `gorm:"type:varchar(20)" json:"f42_immune_hepatitis"`
	Field43ImmunePolio     string `gorm:"type:varchar(20)" json:"f43_immune_polio"`
	Field44ImmuneBCG       string `gorm:"type:varchar(20)" json:"f44_immune_bcg"`
	Field45ImmuneCampak    string `gorm:"type:varchar(20)" json:"f45_immune_campak"`
	Field46ImmuneDPT       string `gorm:"type:varchar(20)" json:"f46_immune_dpt"`
	Field47NoKKS           string `gorm:"type:varchar(30)" json:"f47_no_kks"`
	Field48NoPKH           string `gorm:"type:varchar(30)" json:"f48_no_pkh"`
	Field49NoKIP           string `gorm:"type:varchar(30)" json:"f49_no_kip"`
	Field50IsConfirmed     bool   `json:"f50_is_confirmed"`

	// Status Alur PPDB & Emis
	PPDBStatus         string `gorm:"type:varchar(30);index" json:"ppdb_status"`
	RevisionNotes      string `gorm:"type:text" json:"revision_notes,omitempty"`
	ProfileEditAllowed bool   `gorm:"not null;default:false" json:"profile_edit_allowed"`
	ProfileEditNotes   string `gorm:"type:text" json:"profile_edit_notes,omitempty"`

	// Nomor Induk
	NISLokal string `gorm:"type:varchar(30);index" json:"nis_lokal,omitempty"`
	NISN     string `gorm:"type:varchar(20)" json:"nisn,omitempty"`

	// Status Emis
	EmisStatus string `gorm:"type:varchar(30)" json:"emis_status"`
	EmisNotes  string `gorm:"type:text" json:"emis_notes,omitempty"`

	// Status Siswa & Kelas
	StudentStatus   string `gorm:"type:varchar(30);index" json:"student_status"`
	CurrentClass    string `gorm:"type:varchar(30);index" json:"current_class"`
	StatusKeaktifan string `gorm:"type:varchar(50)" json:"status_keaktifan"`
	FormComplete    bool   `gorm:"-" json:"-"`

	// Kenaikan Kelas / Kelulusan / Pindah
	OutSchoolTarget    string `gorm:"type:varchar(200)" json:"out_school_target,omitempty"`
	OutReason          string `gorm:"type:text" json:"out_reason,omitempty"`
	OutDate            string `gorm:"type:varchar(20)" json:"out_date,omitempty"`
	GraduationYear     string `gorm:"type:varchar(20)" json:"graduation_year,omitempty"`
	CertificateNumber  string `gorm:"type:varchar(50)" json:"certificate_number,omitempty"`
	CertificateScanURL string `gorm:"type:text" json:"certificate_scan_url,omitempty"`
	PhotoURL           string `gorm:"type:text" json:"photo_url,omitempty"`

	// RDM (Raport Digital Madrasah) - stored as JSON text
	RDMSynced bool       `gorm:"default:false" json:"rdm_synced"`
	RDMGrades GradeItems `gorm:"type:text" json:"rdm_grades,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NISNChangeLog keeps an audit trail for every NISN imported or corrected by an operator.
type NISNChangeLog struct {
	ID          string    `gorm:"primaryKey;type:varchar(50)" json:"id"`
	StudentNIK  string    `gorm:"type:varchar(20);index" json:"student_nik"`
	StudentName string    `gorm:"type:varchar(200)" json:"student_name"`
	OldNISN     string    `gorm:"type:varchar(20)" json:"old_nisn"`
	NewNISN     string    `gorm:"type:varchar(20)" json:"new_nisn"`
	ChangedBy   string    `gorm:"type:varchar(20)" json:"changed_by"`
	ChangedAt   time.Time `gorm:"index" json:"changed_at"`
}

// ClassDetail data kelas, tahun ajaran, guru & wali kelas
type ClassDetail struct {
	ID              string    `gorm:"primaryKey;type:varchar(20)" json:"id"`
	ClassName       string    `gorm:"uniqueIndex;type:varchar(30)" json:"class_name"`
	AcademicYear    string    `gorm:"type:varchar(20)" json:"academic_year"`
	TeacherName     string    `gorm:"type:varchar(200)" json:"teacher_name"`
	HomeroomTeacher string    `gorm:"type:varchar(200)" json:"homeroom_teacher"`
	Capacity        int       `json:"capacity"`
	CreatedAt       time.Time `json:"-"`
	UpdatedAt       time.Time `json:"-"`
}

// ActivityLog log aktivitas sistem
type ActivityLog struct {
	ID        string    `gorm:"primaryKey;type:varchar(50)" json:"id"`
	Action    string    `gorm:"type:varchar(200)" json:"action"`
	Detail    string    `gorm:"type:text" json:"detail"`
	Category  string    `gorm:"type:varchar(20);index" json:"category"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// Notification notifikasi sistem
type Notification struct {
	ID        string    `gorm:"primaryKey;type:varchar(50)" json:"id"`
	Title     string    `gorm:"type:varchar(200)" json:"title"`
	Message   string    `gorm:"type:text" json:"message"`
	Type      string    `gorm:"type:varchar(20)" json:"type"`
	IsRead    bool      `gorm:"default:false" json:"is_read"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// DashboardStats statistik dashboard (bukan tabel DB, hanya struct)
type DashboardStats struct {
	TotalPendaftar int `json:"total_pendaftar"`
	SiswaDiterima  int `json:"siswa_diterima"`
	SiswaAktif     int `json:"siswa_aktif"`
	SiswaKeluar    int `json:"siswa_keluar"`
	SiswaLulus     int `json:"siswa_lulus"`
}
