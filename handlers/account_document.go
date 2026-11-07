package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"ppdb-system/models"
	"ppdb-system/storage"
)

// HandleDownloadStudentAccountDocument creates a PDF containing the stored
// account password. Passwords are decrypted only for an authenticated operator.
func (h *AppHandler) HandleDownloadStudentAccountDocument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	admin := h.GetCurrentUser(r)
	if admin == nil {
		http.Error(w, "Silakan login kembali", http.StatusUnauthorized)
		return
	}
	if admin.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Data permintaan tidak valid", http.StatusBadRequest)
		return
	}
	nik := strings.TrimSpace(r.FormValue("nik"))
	student, ok := storage.GlobalStore.GetStudentByNIK(nik)
	if !ok {
		http.Error(w, "Data pendaftar tidak ditemukan", http.StatusNotFound)
		return
	}
	password, err := storage.GlobalStore.GetStudentAccountPassword(nik)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var settings models.PortalSettings
	_ = storage.GlobalStore.DB.First(&settings, 1).Error
	document, err := renderStudentAccountPDF(student, password, settings, storage.GlobalStore.GetSchool())
	if err != nil {
		http.Error(w, "PDF akun gagal dibuat", http.StatusInternalServerError)
		return
	}
	registrationNumber := student.NISLokal
	if registrationNumber == "" {
		registrationNumber = student.ID
	}
	filenameID := safeDocumentFilename(registrationNumber)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="Surat-Akun-`+filenameID+`.pdf"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(document)))
	_, _ = w.Write(document)
}

func renderStudentAccountPDF(student models.StudentForm, password string, settings models.PortalSettings, school models.SchoolProfile) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	translator := pdf.UnicodeTranslatorFromDescriptor("cp1252")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()
	green := settings.DocumentStyle != "minimal"
	if green {
		pdf.SetTextColor(5, 88, 56)
		pdf.SetDrawColor(5, 88, 56)
	} else {
		pdf.SetTextColor(30, 41, 59)
		pdf.SetDrawColor(71, 85, 105)
	}
	header := strings.TrimSpace(settings.DocumentHeader)
	if header == "" {
		header = "KEMENTERIAN AGAMA REPUBLIK INDONESIA"
	}
	title := strings.TrimSpace(settings.AccountPDFTitle)
	if title == "" {
		title = "INFORMASI AKUN SISWA"
	}
	year := student.AcademicYear
	if year == "" {
		year = school.CurrentYear
	}
	pdf.SetFont("Arial", "B", 11)
	pdf.CellFormat(0, 7, translator(header), "", 1, "C", false, 0, "")
	pdf.SetFont("Arial", "B", 15)
	pdf.CellFormat(0, 9, translator(strings.ToUpper(school.SchoolName)), "", 1, "C", false, 0, "")
	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(0, 6, translator("NPSN: "+school.NPSN+"  |  NSM: "+school.NSM), "", 1, "C", false, 0, "")
	pdf.Ln(3)
	pdf.Line(20, pdf.GetY(), 190, pdf.GetY())
	pdf.Ln(8)
	pdf.SetFont("Arial", "B", 14)
	pdf.CellFormat(0, 9, translator(title), "", 1, "C", false, 0, "")
	pdf.SetFont("Arial", "", 10)
	pdf.CellFormat(0, 7, translator("Tahun Ajaran "+strings.ReplaceAll(year, "/", " / ")), "", 1, "C", false, 0, "")
	pdf.Ln(8)

	rows := [][2]string{
		{"Nama Siswa", student.Field01FullName},
		{"NIK untuk Login", student.Field05NIK},
		{"Password Awal", password},
	}
	for _, row := range rows {
		pdf.SetFont("Arial", "B", 10)
		if green {
			pdf.SetFillColor(230, 245, 237)
		} else {
			pdf.SetFillColor(241, 245, 249)
		}
		pdf.CellFormat(48, 10, translator(row[0]), "1", 0, "L", true, 0, "")
		pdf.SetFont("Arial", "", 10)
		pdf.CellFormat(122, 10, translator(row[1]), "1", 1, "L", false, 0, "")
	}
	pdf.Ln(8)
	pdf.SetFont("Arial", "", 9)
	footer := strings.TrimSpace(settings.DocumentFooter)
	if footer == "" {
		footer = "Simpan informasi akun ini dengan baik dan jangan bagikan password kepada orang lain."
	}
	pdf.MultiCell(0, 5, translator(footer), "", "L", false)
	pdf.Ln(8)
	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(0, 6, translator("Dicetak pada "+fmt.Sprintf("%s WIB", nowWIB())), "", 1, "R", false, 0, "")

	var output bytes.Buffer
	if err := pdf.Output(&output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func safeDocumentFilename(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "siswa"
	}
	return b.String()
}

func nowWIB() string {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.Local
	}
	return time.Now().In(loc).Format("02-01-2006 15:04")
}
