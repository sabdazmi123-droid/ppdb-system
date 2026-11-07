package handlers

import (
	"bufio"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"ppdb-system/models"
	"ppdb-system/storage"
)

type AppHandler struct {
	tmplDir         string
	uploadDir       string
	pendingNISNMu   sync.Mutex
	pendingNISNJobs map[string]pendingNISNImport
}

type pendingNISNImport struct {
	ownerNIK string
	records  map[string]storage.NISNImportRow
	expected map[string]storage.NISNImportPreviewRow
	preview  []storage.NISNImportPreviewRow
	expires  time.Time
}

const loginAttemptWindow = 15 * time.Minute
const loginAttemptLimit = 10
const loginAttemptMaxKeys = 10000

var loginAttemptMu sync.Mutex
var loginAttempts = make(map[string][]time.Time)

func reserveLoginAttempt(ip, nik string) bool {
	key := strings.TrimSpace(ip) + "|" + strings.TrimSpace(nik)
	now := time.Now()
	cutoff := now.Add(-loginAttemptWindow)

	loginAttemptMu.Lock()
	defer loginAttemptMu.Unlock()
	for candidate, attempts := range loginAttempts {
		active := attempts[:0]
		for _, at := range attempts {
			if at.After(cutoff) {
				active = append(active, at)
			}
		}
		if len(active) == 0 {
			delete(loginAttempts, candidate)
		} else {
			loginAttempts[candidate] = active
		}
	}
	if len(loginAttempts[key]) >= loginAttemptLimit {
		return false
	}
	if _, exists := loginAttempts[key]; !exists && len(loginAttempts) >= loginAttemptMaxKeys {
		return false
	}
	loginAttempts[key] = append(loginAttempts[key], now)
	return true
}

func clearLoginAttempts(ip, nik string) {
	key := strings.TrimSpace(ip) + "|" + strings.TrimSpace(nik)
	loginAttemptMu.Lock()
	delete(loginAttempts, key)
	loginAttemptMu.Unlock()
}

func NewAppHandler(tmplDir, uploadDir string) *AppHandler {
	_ = os.MkdirAll(uploadDir, 0755)
	return &AppHandler{
		tmplDir:         tmplDir,
		uploadDir:       uploadDir,
		pendingNISNJobs: make(map[string]pendingNISNImport),
	}
}

// Helper untuk session sederhana via Cookie
func (h *AppHandler) GetCurrentUser(r *http.Request) *models.User {
	c, err := r.Cookie("session_user")
	if err != nil || c.Value == "" {
		return nil
	}
	nik := userNIKFromSession(c.Value)
	if nik == "" {
		return nil
	}
	u, ok := storage.GlobalStore.GetUserByNIK(nik)
	if !ok {
		return nil
	}
	return &u
}

func sessionSecret() []byte {
	secret := os.Getenv("PPDB_SESSION_SECRET")
	if secret == "" && os.Getenv("APP_ENV") != "production" {
		secret = "ppdb-development-only-session-secret"
	}
	return []byte(secret)
}

func makeSessionValue(nik string) string {
	encodedNIK := base64.RawURLEncoding.EncodeToString([]byte(nik))
	mac := hmac.New(sha256.New, sessionSecret())
	_, _ = mac.Write([]byte(encodedNIK))
	return encodedNIK + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func userNIKFromSession(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(sessionSecret()) < 32 {
		return ""
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, sessionSecret())
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ""
	}
	nik, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	return string(nik)
}

// RenderTemplate memuat dan merender layout beserta halaman spesifik
func (h *AppHandler) Render(w http.ResponseWriter, r *http.Request, tmplName string, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}

	currentUser := h.GetCurrentUser(r)
	if currentUser == nil {
		// default fallback admin view jika belum login
		admin, ok := storage.GlobalStore.GetUserByNIK("admin")
		if ok {
			currentUser = &admin
		}
	}
	data["CurrentUser"] = currentUser
	if currentUser != nil && currentUser.Role == "student" {
		if st, ok := storage.GlobalStore.GetStudentByNIK(currentUser.NIK); ok {
			data["CurrentStudent"] = st
			if data["Student"] == nil {
				data["Student"] = st
			}
		}
	}
	data["School"] = storage.GlobalStore.GetSchool()
	if data["AcademicYears"] == nil {
		school := storage.GlobalStore.GetSchool()
		data["AcademicYears"] = academicYearOptions(school.CustomAcademicYears, school.CurrentYear)
	}
	data["Stats"] = storage.GlobalStore.GetDashboardStats()
	data["ActiveMenu"] = tmplName
	data["CurrentTimeWIB"] = time.Now().Format("Monday, 02 January 2006 15:04 WIB")

	// Custom template functions
	funcMap := template.FuncMap{
		"formatDate": func(t time.Time) string {
			return t.Format("02 Jan 2006 15:04")
		},
		"formatDateOnly": func(t time.Time) string {
			return t.Format("02 Jan 2006")
		},
		"hasPrefix": strings.HasPrefix,
		"eqStr": func(a, b string) bool {
			return a == b
		},
		"add1": func(i int) int {
			return i + 1
		},
		"jsonEncode": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}

	layoutFile := filepath.Join(h.tmplDir, "layout.html")
	pageFile := filepath.Join(h.tmplDir, tmplName+".html")

	t, err := template.New("layout.html").Funcs(funcMap).ParseFiles(layoutFile, pageFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template Error: %v", err), http.StatusInternalServerError)
		return
	}

	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, fmt.Sprintf("Render Error: %v", err), http.StatusInternalServerError)
	}
}

// Dashboard Page
func (h *AppHandler) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	stats := storage.GlobalStore.GetDashboardStats()
	year := time.Now().Year()
	monthlyRegistrations, monthlyAccepted := storage.GlobalStore.GetDashboardMonthlyStats(year)
	activities := storage.GlobalStore.GetRecentActivities(8)
	notifications := storage.GlobalStore.GetNotifications()

	data := map[string]interface{}{
		"Stats":              stats,
		"ChartYear":          year,
		"ChartRegistrations": monthlyRegistrations,
		"ChartAccepted":      monthlyAccepted,
		"Activities":         activities,
		"Notifications":      notifications,
	}
	h.Render(w, r, "dashboard", data)
}

// HandleGeneratePassword issues a student login password for an applicant.
func (h *AppHandler) HandleGeneratePassword(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	data := map[string]interface{}{}
	if r.Method == http.MethodPost {
		if r.FormValue("action") == "save_ppdb_settings" {
			h.handleGeneratePasswordSettings(w, r)
			return
		}
		nik := strings.TrimSpace(r.FormValue("nik"))
		password := r.FormValue("password")
		if password == "" {
			var passwordBytes [18]byte
			if _, err := cryptorand.Read(passwordBytes[:]); err != nil {
				data["ErrorMsg"] = "Password gagal dibuat. Silakan coba kembali."
			} else {
				password = base64.RawURLEncoding.EncodeToString(passwordBytes[:])
			}
		}
		if data["ErrorMsg"] == nil {
			if len([]byte(password)) < 8 || len([]byte(password)) > 72 {
				data["ErrorMsg"] = "Password harus berisi 8 sampai 72 byte."
			} else {
				school := storage.GlobalStore.GetSchool()
				filled, _, _, configured, full := admissionQuotaStatus(school)
				if configured && full {
					data["ErrorMsg"] = fmt.Sprintf("Kuota tahun ajaran %s sudah penuh (%d/%d). Ubah kuota atau tahun ajaran di pengaturan PPDB.", school.CurrentYear, filled, school.AdmissionQuota)
				} else if err := storage.GlobalStore.CreateStudentAccount(nik, password); err != nil {
					data["ErrorMsg"] = err.Error()
				} else if student, ok := storage.GlobalStore.GetStudentByNIK(nik); ok {
					w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
					data["GeneratedPassword"] = password
					data["GeneratedNIK"] = nik
					data["GeneratedName"] = student.Field01FullName
				}
			}
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	school := storage.GlobalStore.GetSchool()
	filled, remaining, percent, configured, full := admissionQuotaStatus(school)
	data["School"] = school
	data["QuotaFilled"] = filled
	data["QuotaRemaining"] = remaining
	data["QuotaPercent"] = percent
	data["QuotaConfigured"] = configured
	data["QuotaFull"] = full
	ppdbYears := academicYearOptions(school.CustomAcademicYears, school.CurrentYear)
	data["PPDBYears"] = ppdbYears
	data["CustomAcademicYears"] = school.CustomAcademicYears
	var students []models.StudentForm
	yearSet := map[string]struct{}{}
	for _, student := range storage.GlobalStore.GetAllStudents() {
		if student.StudentStatus == "PENDAFTAR" || student.PPDBStatus == "DISETUJUI" {
			students = append(students, student)
			if year := strings.TrimSpace(student.AcademicYear); isValidAcademicYear(year) {
				yearSet[year] = struct{}{}
			}
		}
	}
	sort.SliceStable(students, func(i, j int) bool {
		if students[i].AcademicYear != students[j].AcademicYear {
			return students[i].AcademicYear > students[j].AcademicYear
		}
		return strings.ToLower(students[i].Field01FullName) < strings.ToLower(students[j].Field01FullName)
	})
	data["Students"] = students
	years := make([]string, 0, len(yearSet))
	for _, year := range academicYearOptions(school.CustomAcademicYears, school.CurrentYear) {
		yearSet[year] = struct{}{}
	}
	for year := range yearSet {
		years = append(years, year)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(years)))
	data["AcademicYears"] = years
	if r.Method == http.MethodGet {
		data["SuccessMsg"] = r.URL.Query().Get("success")
		data["ErrorMsg"] = r.URL.Query().Get("error")
	}
	h.Render(w, r, "generate_password", data)
}

func isValidAcademicYear(year string) bool {
	parts := strings.Split(strings.TrimSpace(year), "/")
	if len(parts) != 2 {
		return false
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && start >= 2026 && end == start+1 && len(parts[0]) == 4 && len(parts[1]) == 4
}

func academicYearOptions(custom, current string) []string {
	set := map[string]bool{}
	for year := 2026; year <= 2035; year++ {
		set[fmt.Sprintf("%d/%d", year, year+1)] = true
	}
	for _, year := range strings.Split(custom, ",") {
		year = strings.TrimSpace(year)
		if isValidAcademicYear(year) {
			set[year] = true
		}
	}
	if isValidAcademicYear(current) {
		set[current] = true
	}
	years := make([]string, 0, len(set))
	for year := range set {
		years = append(years, year)
	}
	sort.Strings(years)
	return years
}

func (h *AppHandler) handleGeneratePasswordSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	year := strings.TrimSpace(r.FormValue("academic_year"))
	if !isValidAcademicYear(year) {
		http.Redirect(w, r, "/generate-password?error="+url.QueryEscape("Tahun ajaran tidak valid"), http.StatusSeeOther)
		return
	}
	school := storage.GlobalStore.GetSchool()
	customYears := map[string]bool{}
	for _, option := range strings.Split(school.CustomAcademicYears, ",") {
		option = strings.TrimSpace(option)
		if isValidAcademicYear(option) {
			customYears[option] = true
		}
	}
	for _, option := range strings.Split(r.FormValue("custom_academic_years"), ",") {
		option = strings.TrimSpace(option)
		if isValidAcademicYear(option) {
			customYears[option] = true
		}
	}
	for _, option := range strings.Split(r.FormValue("removed_academic_years"), ",") {
		delete(customYears, strings.TrimSpace(option))
	}
	customList := make([]string, 0, len(customYears))
	for option := range customYears {
		customList = append(customList, option)
	}
	sort.Strings(customList)
	quota, err := strconv.Atoi(strings.TrimSpace(r.FormValue("admission_quota")))
	if err != nil || quota < 0 || quota > 100000 {
		http.Redirect(w, r, "/generate-password?error="+url.QueryEscape("Kuota harus berupa angka antara 0 dan 100.000"), http.StatusSeeOther)
		return
	}
	school.CurrentYear = year
	school.CustomAcademicYears = strings.Join(customList, ",")
	school.AdmissionQuota = quota
	school.RegistrationOpen = r.FormValue("registration_open") == "on"
	filled := quotaFilledForYear(year)
	if school.RegistrationOpen && quota > 0 && filled >= quota {
		// Keep the operator's open/closed preference. Public registration is
		// paused while full and can resume automatically if an applicant is removed.
		if err := storage.GlobalStore.UpdateSchoolSettings(school); err != nil {
			http.Redirect(w, r, "/generate-password?error="+url.QueryEscape("Pengaturan kuota gagal disimpan"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/generate-password?success="+url.QueryEscape(fmt.Sprintf("Pengaturan tersimpan. Kuota %s penuh (%d/%d); pendaftaran otomatis dijeda dan akan terbuka kembali jika kuota tersedia", year, filled, quota)), http.StatusSeeOther)
		return
	}
	if err := storage.GlobalStore.UpdateSchoolSettings(school); err != nil {
		http.Redirect(w, r, "/generate-password?error="+url.QueryEscape("Pengaturan PPDB gagal disimpan"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/generate-password?success="+url.QueryEscape("Tahun ajaran, kuota, dan status pendaftaran berhasil diperbarui"), http.StatusSeeOther)
}

func quotaFilledForYear(year string) int {
	var filled int64
	storage.GlobalStore.DB.Model(&models.StudentForm{}).Where("academic_year = ? AND user_id <> ''", year).Count(&filled)
	return int(filled)
}

func admissionQuotaStatus(school models.SchoolProfile) (filled, remaining, percent int, configured, full bool) {
	filled = quotaFilledForYear(school.CurrentYear)
	configured = school.AdmissionQuota > 0
	if !configured {
		return filled, 0, 0, false, false
	}
	remaining = school.AdmissionQuota - filled
	if remaining < 0 {
		remaining = 0
	}
	percent = filled * 100 / school.AdmissionQuota
	if percent > 100 {
		percent = 100
	}
	full = filled >= school.AdmissionQuota
	return
}

// HandleDeleteApplicant removes an applicant who has not yet become an active student.
func (h *AppHandler) HandleDeleteApplicant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	nik := strings.TrimSpace(r.FormValue("nik"))
	if nik == "" {
		http.Redirect(w, r, "/generate-password?error=NIK+calon+siswa+tidak+valid", http.StatusSeeOther)
		return
	}
	if err := storage.GlobalStore.DeleteApplicantRegistration(nik); err != nil {
		http.Redirect(w, r, "/generate-password?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/generate-password?success="+url.QueryEscape("Data calon siswa dan akun yang sudah dibuat berhasil dihapus; kuota diperbarui otomatis"), http.StatusSeeOther)
}

// Formulir PPDB Page
func (h *AppHandler) HandleFormulirPPDB(w http.ResponseWriter, r *http.Request) {
	var students []models.StudentForm
	for _, student := range storage.GlobalStore.GetAllStudents() {
		// The verification queue only contains applicants who have an account
		// but have not yet been approved or assigned to a class. Active students
		// remain available from the Data Siswa menu.
		hasClass := strings.TrimSpace(student.CurrentClass) != ""
		isAlreadyActive := student.PPDBStatus == "DISETUJUI" || student.StudentStatus == "AKTIF" || student.StudentStatus == "DITERIMA"
		if student.UserID != "" && !hasClass && !isAlreadyActive {
			student.FormComplete = student.Field50IsConfirmed
			students = append(students, student)
		}
	}
	sort.SliceStable(students, func(i, j int) bool {
		if students[i].AcademicYear != students[j].AcademicYear {
			return students[i].AcademicYear > students[j].AcademicYear
		}
		return strings.ToLower(students[i].Field01FullName) < strings.ToLower(students[j].Field01FullName)
	})
	yearSet := map[string]bool{}
	school := storage.GlobalStore.GetSchool()
	for _, year := range academicYearOptions(school.CustomAcademicYears, school.CurrentYear) {
		yearSet[year] = true
	}
	for _, student := range students {
		if isValidAcademicYear(student.AcademicYear) {
			yearSet[student.AcademicYear] = true
		}
	}
	years := make([]string, 0, len(yearSet))
	for year := range yearSet {
		years = append(years, year)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(years)))
	data := map[string]interface{}{
		"Students":      students,
		"AcademicYears": years,
		"SuccessMsg":    r.URL.Query().Get("success"),
		"ErrorMsg":      r.URL.Query().Get("error"),
	}
	h.Render(w, r, "formulir_ppdb", data)
}

// Approve Formulir PPDB Action
func (h *AppHandler) HandleApproveFormulir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := h.GetCurrentUser(r)
	if user == nil || user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	nik := r.FormValue("nik")
	targetClass := r.FormValue("assigned_class")
	admissionDate := strings.TrimSpace(r.FormValue("admission_date"))
	if _, err := time.Parse("2006-01-02", admissionDate); err != nil {
		http.Redirect(w, r, "/formulir-ppdb?error="+url.QueryEscape("Tanggal masuk wajib diisi dengan format tanggal yang benar"), http.StatusSeeOther)
		return
	}
	st, err := storage.GlobalStore.ApproveFormulirPPDB(nik, targetClass, admissionDate)
	if err != nil {
		http.Redirect(w, r, "/formulir-ppdb?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/siswa-aktif?class=%s&success=Formulir+disetujui!+Siswa+resmi+aktif+di+%s+(NIS:+%s)", targetClass, targetClass, st.NISLokal), http.StatusSeeOther)
}

// Reject / Perbaiki Formulir PPDB Action
func (h *AppHandler) HandleRejectFormulir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := h.GetCurrentUser(r)
	if user == nil || user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	nik := r.FormValue("nik")
	notes := r.FormValue("notes")
	if strings.TrimSpace(notes) == "" {
		notes = "Mohon lengkapi berkas dan perbaiki data yang belum sesuai."
	}
	err := storage.GlobalStore.RejectFormulirPPDB(nik, notes)
	if err != nil {
		if r.FormValue("return_to") == "siswa-aktif" {
			http.Redirect(w, r, "/siswa-aktif?class="+url.QueryEscape(r.FormValue("class"))+"&error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/formulir-ppdb?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		}
		return
	}
	if r.FormValue("return_to") == "siswa-aktif" {
		http.Redirect(w, r, "/siswa-aktif?class="+url.QueryEscape(r.FormValue("class"))+"&success="+url.QueryEscape("Akses revisi dibuka; kelas dan status aktif tetap dipertahankan"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/formulir-ppdb?success=Formulir+ditandai+perlu+perbaikan", http.StatusSeeOther)
}

// Siswa Diterima Page
func (h *AppHandler) HandleSiswaDiterima(w http.ResponseWriter, r *http.Request) {
	all := storage.GlobalStore.GetAllStudents()
	var accepted []models.StudentForm
	for _, s := range all {
		if s.StudentStatus == "DITERIMA" || s.StudentStatus == "AKTIF" {
			accepted = append(accepted, s)
		}
	}
	data := map[string]interface{}{
		"Students":   accepted,
		"SuccessMsg": r.URL.Query().Get("success"),
		"ErrorMsg":   r.URL.Query().Get("error"),
	}
	h.Render(w, r, "siswa_diterima", data)
}

// Update EMIS status Action
func (h *AppHandler) HandleUpdateEmis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := h.GetCurrentUser(r)
	if user == nil || user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	nik := r.FormValue("nik")
	action := r.FormValue("action")
	notes := r.FormValue("notes")

	var status string
	switch action {
	case "input":
		status = "DIINPUT"
	case "perbaiki":
		status = "PERBAIKI_EMIS"
	case "approve":
		status = "APPROVED_EMIS"
	case "selesai":
		status = "DIINPUT"
		notes = "Data siswa sudah selesai diinput ke EMIS."
	case "belum":
		status = "BELUM_INPUT"
		notes = ""
	default:
		http.Error(w, "Status EMIS tidak valid", http.StatusBadRequest)
		return
	}

	err := storage.GlobalStore.UpdateEmisStatus(nik, status, notes)
	if err != nil {
		http.Redirect(w, r, "/siswa-diterima?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	msg := "Status Emis berhasil diperbarui."
	if status == "APPROVED_EMIS" {
		msg = "Emis berhasil di-Approve! Kartu Tanda Siswa (KTS) resmi telah diterbitkan."
	} else if status == "DIINPUT" {
		msg = "Status input EMIS ditandai selesai."
	} else if status == "BELUM_INPUT" {
		msg = "Status input EMIS ditandai belum selesai."
	}
	if r.FormValue("return_to") == "siswa-aktif" {
		st, _ := storage.GlobalStore.GetStudentByNIK(nik)
		http.Redirect(w, r, "/siswa-aktif?class="+url.QueryEscape(st.CurrentClass)+"&success="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	if r.FormValue("return_to") == "profile-view" {
		st, _ := storage.GlobalStore.GetStudentByNIK(nik)
		backURL := "/profile-siswa?nik=" + url.QueryEscape(nik) + "&mode=view"
		if st.CurrentClass != "" {
			backURL += "&class=" + url.QueryEscape(st.CurrentClass)
		}
		http.Redirect(w, r, backURL+"&success="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/siswa-diterima?success="+url.QueryEscape(msg), http.StatusSeeOther)
}

// Kartu Siswa Print View
func (h *AppHandler) HandleKartuSiswa(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	nik := r.URL.Query().Get("nik")
	if user.Role == "student" {
		nik = user.NIK
	}
	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Error(w, "Siswa tidak ditemukan", http.StatusNotFound)
		return
	}

	funcMap := template.FuncMap{
		"formatDateOnly": func(t time.Time) string {
			return t.Format("02 Jan 2006")
		},
	}
	pageFile := filepath.Join(h.tmplDir, "kartu_siswa_print.html")
	t, err := template.New("kartu_siswa_print.html").Funcs(funcMap).ParseFiles(pageFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Student":   st,
		"School":    storage.GlobalStore.GetSchool(),
		"DateNow":   time.Now().Format("02 Januari 2006"),
		"AdminView": user.Role != "student",
	}
	_ = t.Execute(w, data)
}

// Data Siswa Aktif Page
func (h *AppHandler) HandleDataSiswaAktif(w http.ResponseWriter, r *http.Request) {
	selectedClass := r.URL.Query().Get("class")
	if r.Method == http.MethodPost {
		user := h.GetCurrentUser(r)
		if user == nil || user.Role == "student" {
			http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
			return
		}
		if r.FormValue("action") != "update_class_staff" {
			http.Error(w, "Aksi tidak valid", http.StatusBadRequest)
			return
		}
		selectedClass = strings.TrimSpace(r.FormValue("class"))
		if err := storage.GlobalStore.UpdateClassStaff(selectedClass, r.FormValue("teacher_name"), r.FormValue("homeroom_teacher")); err != nil {
			http.Redirect(w, r, "/siswa-aktif?class="+url.QueryEscape(selectedClass)+"&error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/siswa-aktif?class="+url.QueryEscape(selectedClass)+"&success="+url.QueryEscape("Data guru dan wali kelas berhasil diperbarui"), http.StatusSeeOther)
		return
	}

	all := storage.GlobalStore.GetAllStudents()
	var classStudents []models.StudentForm
	for _, s := range all {
		if (s.StudentStatus == "AKTIF" || s.StudentStatus == "DITERIMA") && (selectedClass == "" || s.CurrentClass == selectedClass) {
			s.FormComplete = s.Field50IsConfirmed
			classStudents = append(classStudents, s)
		}
	}
	sort.SliceStable(classStudents, func(i, j int) bool {
		return strings.ToLower(classStudents[i].Field01FullName) < strings.ToLower(classStudents[j].Field01FullName)
	})

	school := storage.GlobalStore.GetSchool()
	classes := make([]models.ClassDetail, 0)
	for _, name := range []string{"Kelas A", "Kelas B1", "Kelas B2"} {
		if detail, ok := storage.GlobalStore.GetClassByName(name); ok {
			classes = append(classes, detail)
		} else {
			classes = append(classes, models.ClassDetail{
				ClassName:       name,
				AcademicYear:    school.CurrentYear,
				TeacherName:     "Guru Pengajar " + name,
				HomeroomTeacher: "Wali Kelas " + name,
			})
		}
	}

	var currentClassDetail models.ClassDetail
	if selectedClass != "" {
		var found bool
		currentClassDetail, found = storage.GlobalStore.GetClassByName(selectedClass)
		if !found {
			for _, class := range classes {
				if class.ClassName == selectedClass {
					currentClassDetail = class
					break
				}
			}
		}
	}

	data := map[string]interface{}{
		"SelectedClass":      selectedClass,
		"ClassStudents":      classStudents,
		"Classes":            classes,
		"CurrentClassDetail": currentClassDetail,
		"HasSelectedClass":   selectedClass != "",
		"SuccessMsg":         r.URL.Query().Get("success"),
		"ErrorMsg":           r.URL.Query().Get("error"),
	}
	h.Render(w, r, "siswa_aktif", data)
}

// HandleCetakAbsensi renders a printer-friendly attendance sheet as a table.
func (h *AppHandler) HandleCetakAbsensi(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}

	selectedClass := strings.TrimSpace(r.URL.Query().Get("class"))
	students := make([]models.StudentForm, 0)
	for _, student := range storage.GlobalStore.GetAllStudents() {
		if (student.StudentStatus == "AKTIF" || student.StudentStatus == "DITERIMA") && (selectedClass == "" || student.CurrentClass == selectedClass) {
			students = append(students, student)
		}
	}
	sort.SliceStable(students, func(i, j int) bool {
		return strings.ToLower(students[i].Field01FullName) < strings.ToLower(students[j].Field01FullName)
	})

	classDetail, _ := storage.GlobalStore.GetClassByName(selectedClass)
	page, err := template.New("absensi_siswa_print.html").Funcs(template.FuncMap{
		"add1": func(i int) int { return i + 1 },
	}).ParseFiles(filepath.Join(h.tmplDir, "absensi_siswa_print.html"))
	if err != nil {
		http.Error(w, "Template cetak absensi gagal dimuat", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]interface{}{
		"School": storage.GlobalStore.GetSchool(), "Students": students,
		"SelectedClass": selectedClass, "ClassDetail": classDetail,
		"DateNow": time.Now().Format("02 January 2006"),
	}
	if err := page.Execute(w, data); err != nil {
		http.Error(w, "Dokumen absensi gagal dibuat", http.StatusInternalServerError)
	}
}

// HandleImportNISN previews and applies EMIS NISN imports for active students.
func (h *AppHandler) saveNISNPreview(ownerNIK string, records map[string]storage.NISNImportRow, preview []storage.NISNImportPreviewRow) (string, error) {
	var tokenBytes [32]byte
	if _, err := cryptorand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes[:])
	expected := make(map[string]storage.NISNImportPreviewRow, len(preview))
	for _, row := range preview {
		expected[row.NIK] = row
	}
	h.pendingNISNMu.Lock()
	defer h.pendingNISNMu.Unlock()
	now := time.Now()
	for key, pending := range h.pendingNISNJobs {
		if now.After(pending.expires) {
			delete(h.pendingNISNJobs, key)
		}
	}
	h.pendingNISNJobs[token] = pendingNISNImport{
		ownerNIK: ownerNIK,
		records:  records,
		expected: expected,
		preview:  preview,
		expires:  now.Add(20 * time.Minute),
	}
	return token, nil
}

func (h *AppHandler) consumeNISNPreview(token, ownerNIK string) (pendingNISNImport, error) {
	h.pendingNISNMu.Lock()
	defer h.pendingNISNMu.Unlock()
	pending, exists := h.pendingNISNJobs[token]
	if exists {
		delete(h.pendingNISNJobs, token)
	}
	if !exists || time.Now().After(pending.expires) {
		return pendingNISNImport{}, fmt.Errorf("pratinjau sudah kedaluwarsa; silakan unggah ulang file")
	}
	if pending.ownerNIK != ownerNIK {
		return pendingNISNImport{}, fmt.Errorf("pratinjau ini dibuat oleh akun operator lain")
	}
	return pending, nil
}

func (h *AppHandler) HandleImportNISN(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(5 << 20); err != nil {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("File tidak terbaca atau ukurannya melebihi 5 MB"), http.StatusSeeOther)
				return
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
		} else if err := r.ParseForm(); err != nil {
			http.Error(w, "Permintaan tidak valid", http.StatusBadRequest)
			return
		}

		if r.FormValue("action") == "confirm" {
			pending, err := h.consumeNISNPreview(r.FormValue("preview_token"), user.NIK)
			if err != nil {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
				return
			}
			updated, skipped, err := storage.GlobalStore.ApplyActiveStudentNISN(pending.records, pending.expected, user.NIK)
			if err != nil {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Koreksi dibatalkan: "+err.Error()), http.StatusSeeOther)
				return
			}
			success := url.QueryEscape(fmt.Sprintf("Impor dikonfirmasi: %d NISN diisi/dikoreksi, %d baris dilewati", updated, skipped))
			http.Redirect(w, r, "/pengisian-nisn?success="+success, http.StatusSeeOther)
			return
		}
		if r.FormValue("action") != "preview" || r.MultipartForm == nil {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Pilih file EMIS untuk menampilkan pratinjau"), http.StatusSeeOther)
			return
		}
		file, fileHeader, err := r.FormFile("nisn_file")
		if err != nil {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Pilih file CSV hasil ekspor EMIS terlebih dahulu"), http.StatusSeeOther)
			return
		}
		defer file.Close()

		rows, err := readNISNImportRows(file, fileHeader.Filename)
		if err != nil || len(rows) < 2 {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("File Excel/CSV tidak valid atau tidak berisi data siswa"), http.StatusSeeOther)
			return
		}

		headerIndex := map[string]int{}
		for i, header := range rows[0] {
			header := normalizeImportHeader(header)
			switch header {
			case "nik", "niksiswa", "nikpesertadidik", "nomorindukkependudukan":
				headerIndex["nik"] = i
			case "nisn", "nisnsiswa", "nisnpesertadidik", "nomorinduksiswanasional":
				headerIndex["nisn"] = i
			case "namalengkap", "namalengkapsiswa", "namasiswa":
				headerIndex["name"] = i
			}
		}
		nikColumn, hasNIK := headerIndex["nik"]
		nisnColumn, hasNISN := headerIndex["nisn"]
		nameColumn, hasName := headerIndex["name"]
		if !hasNIK || !hasNISN || !hasName {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Kolom wajib tidak ditemukan. Gunakan header Nama Lengkap, NISN, dan NIK."), http.StatusSeeOther)
			return
		}

		records := make(map[string]storage.NISNImportRow)
		seenNISN := make(map[string]string)
		for i, row := range rows[1:] {
			line := i + 2
			if len(row) <= nikColumn || len(row) <= nisnColumn || len(row) <= nameColumn {
				continue
			}
			name := strings.TrimSpace(row[nameColumn])
			nik := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(row[nikColumn]), "'’"))
			nisn := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(row[nisnColumn]), "'’"))
			if name == "" && nik == "" && nisn == "" {
				continue
			}
			if name == "" {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(fmt.Sprintf("Nama Lengkap pada baris %d masih kosong", line)), http.StatusSeeOther)
				return
			}
			if !isDigits(nik, 16) {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(fmt.Sprintf("NIK pada baris %d harus terdiri dari 16 angka", line)), http.StatusSeeOther)
				return
			}
			if !isDigits(nisn, 10) {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(fmt.Sprintf("NISN pada baris %d harus terdiri dari 10 angka", line)), http.StatusSeeOther)
				return
			}
			if _, exists := records[nik]; exists {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(fmt.Sprintf("NIK duplikat ditemukan pada baris %d", line)), http.StatusSeeOther)
				return
			}
			if previousNIK, exists := seenNISN[nisn]; exists {
				http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape(fmt.Sprintf("NISN duplikat ditemukan pada baris %d (NIK %s)", line, previousNIK)), http.StatusSeeOther)
				return
			}
			records[nik] = storage.NISNImportRow{FullName: name, NISN: nisn}
			seenNISN[nisn] = nik
		}
		if len(records) == 0 {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Tidak ada baris siswa valid untuk diimpor"), http.StatusSeeOther)
			return
		}
		preview, err := storage.GlobalStore.PreviewActiveStudentNISN(records)
		if err != nil {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Pratinjau dibatalkan: "+err.Error()), http.StatusSeeOther)
			return
		}
		token, err := h.saveNISNPreview(user.NIK, records, preview)
		if err != nil {
			http.Redirect(w, r, "/pengisian-nisn?error="+url.QueryEscape("Pratinjau gagal dibuat; silakan ulangi"), http.StatusSeeOther)
			return
		}
		canApply := false
		for _, row := range preview {
			if row.Action == "fill" || row.Action == "replace" {
				canApply = true
				break
			}
		}
		h.Render(w, r, "pengisian_npsn", map[string]interface{}{
			"PreviewRows": preview, "PreviewToken": token, "PreviewHasChanges": canApply,
			"ChangeLogs": storage.GlobalStore.GetRecentNISNChanges(20),
		})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}

	h.Render(w, r, "pengisian_npsn", map[string]interface{}{
		"SuccessMsg": r.URL.Query().Get("success"),
		"ErrorMsg":   r.URL.Query().Get("error"),
		"ChangeLogs": storage.GlobalStore.GetRecentNISNChanges(20),
	})
}

func readNISNImportRows(file io.Reader, filename string) ([][]string, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx":
		workbook, err := excelize.OpenReader(file, excelize.Options{UnzipSizeLimit: 20 << 20, UnzipXMLSizeLimit: 8 << 20})
		if err != nil {
			return nil, err
		}
		defer workbook.Close()
		sheets := workbook.GetSheetList()
		if len(sheets) == 0 {
			return nil, fmt.Errorf("worksheet tidak ditemukan")
		}
		return workbook.GetRows(sheets[0])
	case ".csv":
		buffered := bufio.NewReader(file)
		firstLine, err := buffered.ReadString('\n')
		if err != nil && firstLine == "" {
			return nil, fmt.Errorf("file kosong")
		}
		delimiter := ','
		if strings.Count(firstLine, ";") > strings.Count(firstLine, ",") && strings.Count(firstLine, ";") >= strings.Count(firstLine, "\t") {
			delimiter = ';'
		} else if strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") {
			delimiter = '\t'
		}
		reader := csv.NewReader(io.MultiReader(strings.NewReader(firstLine), buffered))
		reader.Comma = delimiter
		reader.FieldsPerRecord = -1
		return reader.ReadAll()
	default:
		return nil, fmt.Errorf("gunakan file .xlsx atau .csv")
	}
}

func normalizeImportHeader(header string) string {
	header = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header, "\ufeff")))
	return strings.NewReplacer(" ", "", "_", "", "-", "", ".", "").Replace(strings.Join(strings.Fields(header), ""))
}

func isDigits(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// Kenaikan Kelas dan Kelulusan Page
func (h *AppHandler) HandleKenaikanKelulusan(w http.ResponseWriter, r *http.Request) {
	all := storage.GlobalStore.GetAllStudents()
	var activeStudents []models.StudentForm
	for _, s := range all {
		if s.StudentStatus == "AKTIF" || s.StudentStatus == "DITERIMA" {
			activeStudents = append(activeStudents, s)
		}
	}

	data := map[string]interface{}{
		"ActiveStudents": activeStudents,
		"SuccessMsg":     r.URL.Query().Get("success"),
		"ErrorMsg":       r.URL.Query().Get("error"),
	}
	h.Render(w, r, "kenaikan_kelulusan", data)
}

// Proses Aksi Kenaikan Kelas / Ulang / Keluar / Lulus
func (h *AppHandler) HandleProsesKenaikan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := r.FormValue("action")
	nik := r.FormValue("nik")

	switch action {
	case "naik_kelas":
		targetClass := r.FormValue("target_class")
		academicYear := r.FormValue("academic_year")
		if err := storage.GlobalStore.ProcessClassPromotion(nik, targetClass, academicYear); err != nil {
			http.Redirect(w, r, "/kenaikan-kelulusan?error="+err.Error(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/kenaikan-kelulusan?success=Siswa+berhasil+dinaikkan+kelas+ke+"+targetClass, http.StatusSeeOther)

	case "ulang_kelas":
		targetClass := r.FormValue("target_class")
		if err := storage.GlobalStore.ProcessRepeatClass(nik, targetClass); err != nil {
			http.Redirect(w, r, "/kenaikan-kelulusan?error="+err.Error(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/kenaikan-kelulusan?success=Siswa+didaftarkan+mengulang+kelas+di+"+targetClass, http.StatusSeeOther)

	case "pindah_madrasah":
		schoolTarget := r.FormValue("school_target")
		reason := r.FormValue("reason")
		outDate := r.FormValue("out_date")
		if err := storage.GlobalStore.ProcessTransferOut(nik, schoolTarget, reason, outDate); err != nil {
			http.Redirect(w, r, "/kenaikan-kelulusan?error="+err.Error(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/siswa-keluar?success=Siswa+berhasil+dipindahkan+ke+"+schoolTarget, http.StatusSeeOther)

	case "lulus":
		certNo := r.FormValue("cert_number")
		gradYear := r.FormValue("grad_year")

		var scanURL string
		file, header, err := r.FormFile("scan_file")
		if err == nil {
			defer file.Close()
			filename := fmt.Sprintf("ijazah_%s_%d%s", nik, time.Now().Unix(), filepath.Ext(header.Filename))
			destPath := filepath.Join(h.uploadDir, filename)
			out, err := os.Create(destPath)
			if err == nil {
				defer out.Close()
				_, _ = io.Copy(out, file)
				scanURL = "/static/uploads/" + filename
			}
		}

		if err := storage.GlobalStore.ProcessGraduation(nik, certNo, scanURL, gradYear); err != nil {
			http.Redirect(w, r, "/kenaikan-kelulusan?error="+err.Error(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/siswa-lulus?success=Siswa+berhasil+dinyatakan+lulus", http.StatusSeeOther)

	default:
		http.Redirect(w, r, "/kenaikan-kelulusan?error=Aksi+tidak+dikenali", http.StatusSeeOther)
	}
}

// Data Siswa Keluar Page
func (h *AppHandler) HandleDataSiswaKeluar(w http.ResponseWriter, r *http.Request) {
	school := storage.GlobalStore.GetSchool()
	yearFilter := r.URL.Query().Get("year")
	if yearFilter == "" {
		yearFilter = school.CurrentYear
	}

	all := storage.GlobalStore.GetAllStudents()
	var keluarList []models.StudentForm
	for _, s := range all {
		if s.StudentStatus == "PINDAH" {
			if yearFilter == "all" || s.AcademicYear == yearFilter || yearFilter == "" {
				keluarList = append(keluarList, s)
			}
		}
	}

	data := map[string]interface{}{
		"Students":   keluarList,
		"YearFilter": yearFilter,
		"SuccessMsg": r.URL.Query().Get("success"),
		"ErrorMsg":   r.URL.Query().Get("error"),
	}
	h.Render(w, r, "siswa_keluar", data)
}

// Data Siswa Lulus Page
func (h *AppHandler) HandleDataSiswaLulus(w http.ResponseWriter, r *http.Request) {
	yearFilter := r.URL.Query().Get("year")
	if yearFilter == "" {
		yearFilter = "all"
	}

	all := storage.GlobalStore.GetAllStudents()
	var lulusList []models.StudentForm
	for _, s := range all {
		if s.StudentStatus == "LULUS" {
			if yearFilter == "all" || s.GraduationYear == yearFilter || s.AcademicYear == yearFilter {
				lulusList = append(lulusList, s)
			}
		}
	}

	data := map[string]interface{}{
		"Students":   lulusList,
		"YearFilter": yearFilter,
		"SuccessMsg": r.URL.Query().Get("success"),
		"ErrorMsg":   r.URL.Query().Get("error"),
	}
	h.Render(w, r, "siswa_lulus", data)
}

// Import Siswa Lulus Tahun Sebelumnya
func (h *AppHandler) HandleImportLulus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nik := r.FormValue("nik")
	name := r.FormValue("name")
	gradYear := r.FormValue("grad_year")
	certNo := r.FormValue("cert_number")
	nisn := r.FormValue("nisn")
	nisLokal := r.FormValue("nis_lokal")

	st := models.StudentForm{
		Field05NIK:        nik,
		Field01FullName:   name,
		GraduationYear:    gradYear,
		CertificateNumber: certNo,
		NISN:              nisn,
		NISLokal:          nisLokal,
		CurrentClass:      "Kelas B2 (Alumni)",
		StudentStatus:     "LULUS",
		StatusKeaktifan:   "Lulus",
	}

	if err := storage.GlobalStore.ImportPastGraduate(st); err != nil {
		http.Redirect(w, r, "/siswa-lulus?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/siswa-lulus?success=Data+alumni+tahun+sebelumnya+berhasil+diimport!", http.StatusSeeOther)
}

// Profil & Formulir Lengkap Siswa (50 Field)
func (h *AppHandler) HandleProfileSiswa(w http.ResponseWriter, r *http.Request) {
	currentUser := h.GetCurrentUser(r)
	if currentUser == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	nik := r.URL.Query().Get("nik")
	if currentUser.Role == "student" {
		nik = currentUser.NIK
	}

	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Redirect(w, r, "/formulir-ppdb?error=Data+siswa+tidak+ditemukan", http.StatusSeeOther)
		return
	}
	if currentUser.Role != "student" && r.Method == http.MethodGet && r.URL.Query().Get("mode") == "view" {
		backClass := strings.TrimSpace(r.URL.Query().Get("class"))
		if backClass == "" {
			backClass = st.CurrentClass
		}
		backURL := "/siswa-aktif"
		if backClass != "" {
			backURL += "?class=" + url.QueryEscape(backClass)
		}
		h.Render(w, r, "profile_siswa_view", map[string]interface{}{
			"Student": st, "Sections": studentProfileSections(st), "BackURL": backURL,
			"SuccessMsg": r.URL.Query().Get("success"),
		})
		return
	}
	// Older public registrations were incorrectly marked as submitted before
	// the 50-field profile was filled. An unconfirmed pending record is still a
	// draft and must remain editable until the student submits the complete form.
	wasUnsubmittedPendingForm := st.PPDBStatus == "MENUNGGU_VERIFIKASI" && !st.Field50IsConfirmed
	canEditProfile := currentUser == nil || currentUser.Role != "student" || st.PPDBStatus == "DRAFT" || st.PPDBStatus == "PERLU_PERBAIKAN" || st.ProfileEditAllowed || wasUnsubmittedPendingForm

	if r.Method == http.MethodPost {
		if !canEditProfile {
			http.Redirect(w, r, fmt.Sprintf("/profile-siswa?nik=%s&error=Formulir+sudah+diajukan+dan+sedang+dikunci", nik), http.StatusSeeOther)
			return
		}
		st.Field01FullName = r.FormValue("f01_full_name")
		st.Field02BirthPlace = r.FormValue("f02_birth_place")
		st.Field03BirthDate = r.FormValue("f03_birth_date")
		st.Field04Nationality = r.FormValue("f04_nationality")
		st.Field06Gender = r.FormValue("f06_gender")
		st.Field07Religion = r.FormValue("f07_religion")
		st.Field08ChildOrder, _ = strconv.Atoi(r.FormValue("f08_child_order"))
		st.Field09SiblingsCount, _ = strconv.Atoi(r.FormValue("f09_siblings_count"))
		st.Field10Distance = r.FormValue("f10_distance")
		st.Field11Transport = r.FormValue("f11_transport")
		st.Field12TravelTimeMin, _ = strconv.Atoi(r.FormValue("f12_travel_time_min"))
		st.Field13PhoneWA = r.FormValue("f13_phone_wa")
		st.Field14NoKK = r.FormValue("f14_no_kk")
		st.Field15FamilyHead = r.FormValue("f15_family_head")
		st.Field16FatherName = r.FormValue("f16_father_name")
		st.Field17FatherStatus = r.FormValue("f17_father_status")
		st.Field18FatherNIK = r.FormValue("f18_father_nik")
		st.Field19FatherBirthPl = r.FormValue("f19_father_birth_pl")
		st.Field20FatherBirthYr = r.FormValue("f20_father_birth_yr")
		st.Field21FatherEdu = r.FormValue("f21_father_edu")
		st.Field22FatherJob = r.FormValue("f22_father_job")
		st.Field23FatherIncome = r.FormValue("f23_father_income")
		st.Field24ResidenceType = r.FormValue("f24_residence_type")
		st.Field25HouseOwnership = r.FormValue("f25_house_ownership")
		st.Field26Province = r.FormValue("f26_province")
		st.Field27City = r.FormValue("f27_city")
		st.Field28District = r.FormValue("f28_district")
		st.Field29Village = r.FormValue("f29_village")
		st.Field30StreetAddress = r.FormValue("f30_street_address")
		st.Field31PostalCode = r.FormValue("f31_postal_code")
		st.Field32MotherName = r.FormValue("f32_mother_name")
		st.Field33MotherStatus = r.FormValue("f33_mother_status")
		st.Field34MotherNIK = r.FormValue("f34_mother_nik")
		st.Field35MotherBirthPl = r.FormValue("f35_mother_birth_pl")
		st.Field36MotherBirthYr = r.FormValue("f36_mother_birth_yr")
		st.Field37MotherEdu = r.FormValue("f37_mother_edu")
		st.Field38MotherJob = r.FormValue("f38_mother_job")
		st.Field39MotherIncome = r.FormValue("f39_mother_income")
		st.Field40AttendedPAUD = r.FormValue("f40_attended_paud")
		st.Field41AttendedTKRA = r.FormValue("f41_attended_tkra")
		st.Field42ImmuneHepatitis = r.FormValue("f42_immune_hepatitis")
		st.Field43ImmunePolio = r.FormValue("f43_immune_polio")
		st.Field44ImmuneBCG = r.FormValue("f44_immune_bcg")
		st.Field45ImmuneCampak = r.FormValue("f45_immune_campak")
		st.Field46ImmuneDPT = r.FormValue("f46_immune_dpt")
		st.Field47NoKKS = r.FormValue("f47_no_kks")
		st.Field48NoPKH = r.FormValue("f48_no_pkh")
		st.Field49NoKIP = r.FormValue("f49_no_kip")
		st.Field50IsConfirmed = r.FormValue("f50_is_confirmed") == "on" || r.FormValue("f50_is_confirmed") == "true"

		// Validasi ulang di server supaya data wajib dan format identitas tidak bisa dilewati.
		requiredFields := []struct {
			name  string
			label string
		}{
			{"f01_full_name", "Nama lengkap siswa"}, {"f02_birth_place", "Tempat lahir siswa"}, {"f03_birth_date", "Tanggal lahir siswa"},
			{"f04_nationality", "Kewarganegaraan"}, {"f05_nik", "NIK siswa"}, {"f06_gender", "Jenis kelamin"}, {"f07_religion", "Agama"},
			{"f08_child_order", "Anak ke"}, {"f09_siblings_count", "Jumlah saudara"}, {"f11_transport", "Transportasi"},
			{"f12_travel_time_min", "Waktu tempuh"}, {"f13_phone_wa", "Nomor HP/WhatsApp"}, {"f14_no_kk", "Nomor KK"},
			{"f15_family_head", "Nama kepala keluarga"}, {"f16_father_name", "Nama ayah"}, {"f17_father_status", "Status ayah"},
			{"f18_father_nik", "NIK ayah"}, {"f19_father_birth_pl", "Tempat lahir ayah"}, {"f20_father_birth_yr", "Tanggal lahir ayah"},
			{"f21_father_edu", "Pendidikan ayah"}, {"f22_father_job", "Pekerjaan ayah"}, {"f23_father_income", "Penghasilan ayah"},
			{"f24_residence_type", "Jenis tempat tinggal"}, {"f25_house_ownership", "Status kepemilikan rumah"}, {"f26_province", "Provinsi"},
			{"f27_city", "Kabupaten/kota"}, {"f28_district", "Kecamatan"}, {"f29_village", "Desa/kelurahan"}, {"f30_street_address", "Alamat tinggal"},
			{"f31_postal_code", "Kode pos"}, {"f32_mother_name", "Nama ibu"}, {"f33_mother_status", "Status ibu"}, {"f34_mother_nik", "NIK ibu"},
			{"f35_mother_birth_pl", "Tempat lahir ibu"}, {"f36_mother_birth_yr", "Tanggal lahir ibu"}, {"f37_mother_edu", "Pendidikan ibu"},
			{"f38_mother_job", "Pekerjaan ibu"}, {"f39_mother_income", "Penghasilan ibu"}, {"f40_attended_paud", "Riwayat sekolah PAUD"},
			{"f41_attended_tkra", "Riwayat sekolah TK/RA"}, {"f47_no_kks", "Nomor KKS (isi 0 jika tidak punya)"},
			{"f48_no_pkh", "Nomor PKH (isi 0 jika tidak punya)"}, {"f49_no_kip", "Nomor KIP (isi 0 jika tidak punya)"},
		}
		var validationErrors []string
		for _, field := range requiredFields {
			if strings.TrimSpace(r.FormValue(field.name)) == "" {
				validationErrors = append(validationErrors, field.label)
			}
		}
		for _, field := range []struct{ name, label string }{{"f05_nik", "NIK siswa"}, {"f14_no_kk", "Nomor KK"}, {"f18_father_nik", "NIK ayah"}, {"f34_mother_nik", "NIK ibu"}} {
			value := strings.TrimSpace(r.FormValue(field.name))
			if value != "" && (len(value) != 16 || !isDigitsOnly(value)) {
				validationErrors = append(validationErrors, field.label+" harus terdiri dari 16 angka")
			}
		}
		phone := strings.TrimSpace(r.FormValue("f13_phone_wa"))
		if phone != "" && (!isDigitsOnly(phone) || len(phone) < 10 || len(phone) > 15) {
			validationErrors = append(validationErrors, "Nomor HP/WhatsApp harus 10–15 angka")
		}
		postalCode := strings.TrimSpace(r.FormValue("f31_postal_code"))
		if postalCode != "" && (len(postalCode) != 5 || !isDigitsOnly(postalCode)) {
			validationErrors = append(validationErrors, "Kode pos harus terdiri dari 5 angka")
		}
		if r.FormValue("f50_is_confirmed") != "on" && r.FormValue("f50_is_confirmed") != "true" {
			validationErrors = append(validationErrors, "Pernyataan kebenaran data harus dicentang")
		}
		if len(validationErrors) > 0 {
			h.Render(w, r, "profile_siswa", map[string]interface{}{
				"Student": st, "ErrorMsg": "Periksa data yang ditandai. " + strings.Join(validationErrors, "; "), "CanEditProfile": true,
			})
			return
		}

		// Siswa aktif yang diberi akses perbaikan tetap mempertahankan kelas dan status diterimanya.
		approvedStudentRepair := (st.ProfileEditAllowed || st.PPDBStatus == "PERLU_PERBAIKAN") && (st.PPDBStatus == "DISETUJUI" || st.PPDBStatus == "PERLU_PERBAIKAN" || st.StudentStatus == "AKTIF" || st.StudentStatus == "DITERIMA")
		if approvedStudentRepair {
			st.PPDBStatus = "DISETUJUI"
			st.ProfileEditAllowed = false
			st.ProfileEditNotes = ""
			st.RevisionNotes = ""
		} else if st.PPDBStatus == "PERLU_PERBAIKAN" || st.PPDBStatus == "DRAFT" || wasUnsubmittedPendingForm {
			st.PPDBStatus = "MENUNGGU_VERIFIKASI"
			st.RevisionNotes = ""
		}

		if err := storage.GlobalStore.UpdateStudentForm(nik, st); err != nil {
			h.Render(w, r, "profile_siswa", map[string]interface{}{"Student": st, "ErrorMsg": err.Error(), "CanEditProfile": true})
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/profile-siswa?nik=%s&success=Perubahan+formulir+berhasil+disimpan!", nik), http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"Student":        st,
		"SuccessMsg":     r.URL.Query().Get("success"),
		"ErrorMsg":       r.URL.Query().Get("error"),
		"CanEditProfile": canEditProfile,
	}
	h.Render(w, r, "profile_siswa", data)
}

type studentProfileRow struct {
	Label string
	Value string
}

type studentProfileSection struct {
	Title string
	Rows  []studentProfileRow
}

func studentProfileSections(st models.StudentForm) []studentProfileSection {
	value := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return "-"
		}
		return s
	}
	number := func(n int) string { return strconv.Itoa(n) }

	return []studentProfileSection{
		{Title: "Data Diri", Rows: []studentProfileRow{
			{"Nama Lengkap", value(st.Field01FullName)}, {"Nama Panggilan", value(st.ApplicantNickName)},
			{"NIK", value(st.Field05NIK)}, {"NISN", value(st.NISN)}, {"NIS Lokal", value(st.NISLokal)},
			{"Jenis Kelamin", value(st.Field06Gender)}, {"Tempat Lahir", value(st.Field02BirthPlace)},
			{"Tanggal Lahir", value(st.Field03BirthDate)}, {"Kewarganegaraan", value(st.Field04Nationality)},
			{"Agama", value(st.Field07Religion)}, {"Tahun Ajaran", value(st.AcademicYear)},
			{"Tanggal Masuk", value(st.AdmissionDate)},
		}},
		{Title: "Alamat dan Keluarga", Rows: []studentProfileRow{
			{"Nomor Kartu Keluarga", value(st.Field14NoKK)}, {"Kepala Keluarga", value(st.Field15FamilyHead)},
			{"Provinsi", value(st.Field26Province)}, {"Kabupaten / Kota", value(st.Field27City)},
			{"Kecamatan", value(st.Field28District)}, {"Desa / Kelurahan", value(st.Field29Village)},
			{"Alamat", value(st.Field30StreetAddress)}, {"Kode Pos", value(st.Field31PostalCode)},
			{"Nomor WhatsApp", value(st.Field13PhoneWA)}, {"Jenis Tempat Tinggal", value(st.Field24ResidenceType)},
			{"Status Kepemilikan Rumah", value(st.Field25HouseOwnership)},
		}},
		{Title: "Data Ayah", Rows: []studentProfileRow{
			{"Nama Ayah", value(st.Field16FatherName)}, {"Status Ayah", value(st.Field17FatherStatus)},
			{"NIK Ayah", value(st.Field18FatherNIK)}, {"Tempat Lahir Ayah", value(st.Field19FatherBirthPl)},
			{"Tanggal Lahir Ayah", value(st.Field20FatherBirthYr)}, {"Pendidikan Ayah", value(st.Field21FatherEdu)},
			{"Pekerjaan Ayah", value(st.Field22FatherJob)}, {"Penghasilan Ayah", value(st.Field23FatherIncome)},
		}},
		{Title: "Data Ibu", Rows: []studentProfileRow{
			{"Nama Ibu", value(st.Field32MotherName)}, {"Status Ibu", value(st.Field33MotherStatus)},
			{"NIK Ibu", value(st.Field34MotherNIK)}, {"Tempat Lahir Ibu", value(st.Field35MotherBirthPl)},
			{"Tanggal Lahir Ibu", value(st.Field36MotherBirthYr)}, {"Pendidikan Ibu", value(st.Field37MotherEdu)},
			{"Pekerjaan Ibu", value(st.Field38MotherJob)}, {"Penghasilan Ibu", value(st.Field39MotherIncome)},
		}},
		{Title: "Data Pendidikan dan Kesejahteraan", Rows: []studentProfileRow{
			{"Anak ke", number(st.Field08ChildOrder)}, {"Jumlah Saudara", number(st.Field09SiblingsCount)},
			{"Jarak Tempat Tinggal", value(st.Field10Distance)}, {"Transportasi", value(st.Field11Transport)},
			{"Waktu Tempuh", fmt.Sprintf("%d menit", st.Field12TravelTimeMin)},
			{"Pernah Mengikuti PAUD", value(st.Field40AttendedPAUD)}, {"Pernah Mengikuti TK/RA", value(st.Field41AttendedTKRA)},
			{"Imunisasi Hepatitis", value(st.Field42ImmuneHepatitis)}, {"Imunisasi Polio", value(st.Field43ImmunePolio)},
			{"Imunisasi BCG", value(st.Field44ImmuneBCG)}, {"Imunisasi Campak", value(st.Field45ImmuneCampak)},
			{"Imunisasi DPT", value(st.Field46ImmuneDPT)}, {"Nomor KKS", value(st.Field47NoKKS)},
			{"Nomor PKH", value(st.Field48NoPKH)}, {"Nomor KIP", value(st.Field49NoKIP)},
		}},
		{Title: "Status Madrasah", Rows: []studentProfileRow{
			{"Status Formulir PPDB", value(st.PPDBStatus)}, {"Status Siswa", value(st.StatusKeaktifan)},
			{"Kelas", value(st.CurrentClass)}, {"Status Input EMIS", value(st.EmisStatus)},
			{"Catatan Perbaikan", value(st.ProfileEditNotes)},
		}},
	}
}

func isDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Cetak Formulir Pendaftaran PDF / Print layout
func (h *AppHandler) HandleCetakFormulir(w http.ResponseWriter, r *http.Request) {
	nik := r.URL.Query().Get("nik")
	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Error(w, "Data siswa tidak ditemukan", http.StatusNotFound)
		return
	}

	pageFile := filepath.Join(h.tmplDir, "cetak_formulir_print.html")
	t, err := template.ParseFiles(pageFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Student":  st,
		"School":   storage.GlobalStore.GetSchool(),
		"Settings": documentSettings(),
		"DateNow":  time.Now().Format("02 Januari 2006"),
	}
	_ = t.Execute(w, data)
}

// Login & Auth
func (h *AppHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		nik := strings.TrimSpace(r.FormValue("nik"))
		pass := strings.TrimSpace(r.FormValue("password"))
		ip := clientIP(r)
		if !reserveLoginAttempt(ip, nik) {
			http.Redirect(w, r, "/login?error=Terlalu+banyak+percobaan+login.+Silakan+coba+lagi+15+menit+kemudian", http.StatusSeeOther)
			return
		}

		u, exists := storage.GlobalStore.GetUserByNIK(nik)
		if !exists || !storage.CheckPassword(u.PasswordHash, pass) {
			http.Redirect(w, r, "/login?error=NIK+atau+Password+salah", http.StatusSeeOther)
			return
		}
		clearLoginAttempts(ip, nik)

		// Set cookie session
		http.SetCookie(w, &http.Cookie{
			Name:     "session_user",
			Value:    makeSessionValue(u.NIK),
			Path:     "/",
			Expires:  time.Now().Add(24 * time.Hour),
			HttpOnly: true,
			Secure:   os.Getenv("APP_ENV") == "production",
			SameSite: http.SameSiteLaxMode,
		})

		if u.Role == "student" {
			http.Redirect(w, r, "/profile-siswa?nik="+u.NIK, http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		}
		return
	}

	data := map[string]interface{}{
		"SuccessMsg": r.URL.Query().Get("success"),
		"ErrorMsg":   r.URL.Query().Get("error"),
	}
	pageFile := filepath.Join(h.tmplDir, "login_portal.html")
	t, err := template.ParseFiles(pageFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = t.Execute(w, data)
}

// Register Siswa Baru menerima data calon siswa tanpa membuat akun terlebih dahulu.
func (h *AppHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	school := storage.GlobalStore.GetSchool()
	filled, remaining, percent, configured, full := admissionQuotaStatus(school)
	closed := !school.RegistrationOpen || full
	if closed {
		data := map[string]interface{}{
			"RegistrationClosed": true, "CurrentYear": school.CurrentYear, "School": school,
			"QuotaFilled": filled, "QuotaRemaining": remaining, "QuotaPercent": percent,
			"QuotaConfigured": configured, "QuotaFull": full,
			"ErrorMsg": r.URL.Query().Get("error"),
		}
		if r.Method == http.MethodPost {
			message := "Pendaftaran sedang ditutup oleh admin"
			if full {
				message = fmt.Sprintf("Kuota pendaftaran tahun ajaran %s sudah penuh", school.CurrentYear)
			}
			http.Redirect(w, r, "/register?error="+url.QueryEscape(message), http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
			return
		}
		pageFile := filepath.Join(h.tmplDir, "register.html")
		t, err := template.ParseFiles(pageFile)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = t.Execute(w, data)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Data formulir tidak valid", http.StatusBadRequest)
			return
		}
		nik := strings.TrimSpace(r.FormValue("nik"))
		// Tahun ajaran pendaftaran selalu mengikuti pengaturan aktif admin.
		// Jangan percaya nilai dari form publik karena bisa diubah dari browser.
		academicYear := strings.TrimSpace(school.CurrentYear)
		if !isValidAcademicYear(academicYear) {
			http.Redirect(w, r, "/register?error="+url.QueryEscape("Silakan pilih tahun ajaran yang tersedia"), http.StatusSeeOther)
			return
		}
		student := models.StudentForm{
			Field01FullName:    strings.ToUpper(strings.TrimSpace(r.FormValue("full_name"))),
			ApplicantNickName:  strings.ToUpper(strings.TrimSpace(r.FormValue("nick_name"))),
			ApplicantEmail:     strings.ToLower(strings.TrimSpace(r.FormValue("email"))),
			Field05NIK:         nik,
			Field02BirthPlace:  strings.ToUpper(strings.TrimSpace(r.FormValue("birth_place"))),
			Field03BirthDate:   strings.TrimSpace(r.FormValue("birth_date")),
			Field06Gender:      strings.TrimSpace(r.FormValue("gender")),
			Field13PhoneWA:     strings.TrimSpace(r.FormValue("phone")),
			Field32MotherName:  strings.ToUpper(strings.TrimSpace(r.FormValue("mother_name"))),
			AcademicYear:       academicYear,
			Field04Nationality: "WARGA NEGARA INDONESIA / WNI",
			Field07Religion:    "ISLAM",
			Field08ChildOrder:  1,
			PPDBStatus:         "MENUNGGU_VERIFIKASI",
			EmisStatus:         "BELUM_INPUT",
			StudentStatus:      "PENDAFTAR",
			StatusKeaktifan:    "Calon Siswa",
		}
		if !isDigitsOnly(nik) || len(nik) != 16 {
			http.Redirect(w, r, "/register?error="+url.QueryEscape("NIK harus terdiri dari 16 angka"), http.StatusSeeOther)
			return
		}
		if student.Field01FullName == "" || student.ApplicantNickName == "" || student.Field02BirthPlace == "" || student.Field03BirthDate == "" || student.Field06Gender == "" || student.Field32MotherName == "" || student.Field13PhoneWA == "" || student.ApplicantEmail == "" {
			http.Redirect(w, r, "/register?error="+url.QueryEscape("Mohon lengkapi semua data wajib"), http.StatusSeeOther)
			return
		}
		if _, exists := storage.GlobalStore.GetUserByNIK(nik); exists {
			http.Redirect(w, r, "/register?error="+url.QueryEscape("NIK ini sudah memiliki akun"), http.StatusSeeOther)
			return
		}
		if err := storage.GlobalStore.CreatePublicRegistration(student); err != nil {
			http.Redirect(w, r, "/register?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login?success="+url.QueryEscape("Data pendaftaran berhasil dikirim. Admin akan memeriksa dan membuat password akun Anda."), http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	data := map[string]interface{}{
		"ErrorMsg":    r.URL.Query().Get("error"),
		"CurrentYear": school.CurrentYear,
		"QuotaFilled": filled, "QuotaRemaining": remaining, "QuotaPercent": percent,
		"QuotaConfigured": configured, "QuotaFull": full,
	}
	pageFile := filepath.Join(h.tmplDir, "register.html")
	t, err := template.ParseFiles(pageFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = t.Execute(w, data)
}

// HandlePPDBSettings lets an operator close registration and update the active academic year.
func (h *AppHandler) HandlePPDBSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/generate-password", http.StatusSeeOther)
}

// HandleChangePassword lets an authenticated user update their own password.
func (h *AppHandler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		currentPassword := strings.TrimSpace(r.FormValue("current_password"))
		newPassword := strings.TrimSpace(r.FormValue("new_password"))
		confirmPassword := strings.TrimSpace(r.FormValue("confirm_password"))
		if !storage.CheckPassword(user.PasswordHash, currentPassword) {
			http.Redirect(w, r, "/ubah-kata-sandi?error=Kata+sandi+lama+salah", http.StatusSeeOther)
			return
		}
		if len([]byte(newPassword)) < 8 || len([]byte(newPassword)) > 72 {
			http.Redirect(w, r, "/ubah-kata-sandi?error=Kata+sandi+baru+harus+8+sampai+72+byte", http.StatusSeeOther)
			return
		}
		if newPassword != confirmPassword {
			http.Redirect(w, r, "/ubah-kata-sandi?error=Konfirmasi+kata+sandi+tidak+cocok", http.StatusSeeOther)
			return
		}
		if err := storage.GlobalStore.DB.Model(&models.User{}).Where("nik = ?", user.NIK).Update("password_hash", storage.HashPassword(newPassword)).Error; err != nil {
			http.Error(w, "Kata sandi gagal diperbarui.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/ubah-kata-sandi?success=Kata+sandi+berhasil+diubah", http.StatusSeeOther)
		return
	}
	h.Render(w, r, "ubah_kata_sandi", map[string]interface{}{
		"ErrorMsg":   r.URL.Query().Get("error"),
		"SuccessMsg": r.URL.Query().Get("success"),
	})
}

// Logout
func (h *AppHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_user",
		Value:    "",
		Path:     "/",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Secure:   os.Getenv("APP_ENV") == "production",
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Student Menu: Status Siswa & KTS
func (h *AppHandler) HandleStudentStatus(w http.ResponseWriter, r *http.Request) {
	u := h.GetCurrentUser(r)
	nik := r.URL.Query().Get("nik")
	if nik == "" && u != nil {
		nik = u.NIK
	}
	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Redirect(w, r, "/login?error=Data+siswa+tidak+ditemukan", http.StatusSeeOther)
		return
	}
	data := map[string]interface{}{
		"Student": st,
	}
	h.Render(w, r, "student_status", data)
}

// Student Menu: Raport Digital Madrasah (RDM)
func (h *AppHandler) HandleStudentRDM(w http.ResponseWriter, r *http.Request) {
	u := h.GetCurrentUser(r)
	nik := r.URL.Query().Get("nik")
	if nik == "" && u != nil {
		nik = u.NIK
	}
	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Redirect(w, r, "/login?error=Data+siswa+tidak+ditemukan", http.StatusSeeOther)
		return
	}
	data := map[string]interface{}{
		"Student":       st,
		"DefaultGrades": storage.GetDefaultRDMGrades(),
	}
	h.Render(w, r, "student_rdm", data)
}

// Admin / Guru Menu: Input & Kelola Nilai RDM Siswa
func (h *AppHandler) HandleAdminRDMInput(w http.ResponseWriter, r *http.Request) {
	nik := r.URL.Query().Get("nik")
	if r.Method == http.MethodPost {
		nik = r.FormValue("nik")
		grades := make([]models.GradeItem, 0)
		for i := 0; i < 10; i++ {
			subj := r.FormValue(fmt.Sprintf("subject_%d", i))
			if subj == "" {
				continue
			}
			score, _ := strconv.Atoi(r.FormValue(fmt.Sprintf("score_%d", i)))
			pred := r.FormValue(fmt.Sprintf("predicate_%d", i))
			desc := r.FormValue(fmt.Sprintf("description_%d", i))
			grades = append(grades, models.GradeItem{
				SubjectName: subj,
				Score:       score,
				Predicate:   pred,
				Description: desc,
			})
		}
		if len(grades) > 0 {
			_ = storage.GlobalStore.SaveRDMGrades(nik, grades)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/rdm-input?nik=%s&success=Nilai+RDM+berhasil+disimpan+dan+disinkronkan+ke+akun+siswa!", nik), http.StatusSeeOther)
		return
	}

	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Redirect(w, r, "/siswa-aktif?error=Data+siswa+tidak+ditemukan", http.StatusSeeOther)
		return
	}

	grades := []models.GradeItem(st.RDMGrades)
	if len(grades) == 0 {
		grades = storage.GetDefaultRDMGrades()
	}

	data := map[string]interface{}{
		"Student":    st,
		"Grades":     grades,
		"SuccessMsg": r.URL.Query().Get("success"),
	}
	h.Render(w, r, "admin_rdm_input", data)
}

// Student Menu: Ijazah & Berkas
func (h *AppHandler) HandleStudentIjazah(w http.ResponseWriter, r *http.Request) {
	u := h.GetCurrentUser(r)
	nik := r.URL.Query().Get("nik")
	if nik == "" && u != nil {
		nik = u.NIK
	}
	st, exists := storage.GlobalStore.GetStudentByNIK(nik)
	if !exists {
		http.Redirect(w, r, "/login?error=Data+siswa+tidak+ditemukan", http.StatusSeeOther)
		return
	}
	data := map[string]interface{}{
		"Student": st,
	}
	h.Render(w, r, "student_ijazah", data)
}

// API Sync RDM
func (h *AppHandler) HandleSyncRDM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	nik := r.URL.Query().Get("nik")
	if nik != "" {
		st, ok := storage.GlobalStore.GetStudentByNIK(nik)
		if ok {
			st.RDMSynced = true
			_ = storage.GlobalStore.UpdateStudentForm(nik, st)
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Sinkronisasi ke Raport Digital Madrasah (RDM) Berhasil!",
		"time":    time.Now().Format("15:04:05 WIB"),
	})
}
