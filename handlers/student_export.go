package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xuri/excelize/v2"
	"ppdb-system/models"
	"ppdb-system/storage"
)

func (h *AppHandler) HandleExportActiveStudents(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil {
		http.Error(w, "Silakan login kembali", http.StatusUnauthorized)
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

	book := excelize.NewFile()
	defer book.Close()
	const sheet = "Siswa Aktif"
	book.SetSheetName("Sheet1", sheet)
	headers := []string{"No", "Kelas", "Tanggal Masuk", "NIS Lokal", "NISN", "Nama Lengkap", "NIK", "Jenis Kelamin", "Tempat Lahir", "Tanggal Lahir", "Nomor WhatsApp", "Tahun Ajaran", "Status Siswa", "Status EMIS"}
	for i, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = book.SetCellValue(sheet, cell, header)
	}
	for row, student := range students {
		values := []interface{}{row + 1, student.CurrentClass, student.AdmissionDate, student.NISLokal, student.NISN,
			student.Field01FullName, student.Field05NIK, student.Field06Gender, student.Field02BirthPlace,
			student.Field03BirthDate, student.Field13PhoneWA, student.AcademicYear, student.StatusKeaktifan, student.EmisStatus}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, row+2)
			_ = book.SetCellValue(sheet, cell, value)
		}
	}
	style, err := book.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Color: []string{"087454"}, Pattern: 1}, Alignment: &excelize.Alignment{Vertical: "center"}})
	if err == nil {
		_ = book.SetCellStyle(sheet, "A1", "N1", style)
	}
	widths := []float64{8, 14, 16, 24, 18, 28, 20, 16, 20, 16, 20, 16, 20, 18}
	for i, width := range widths {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = book.SetColWidth(sheet, col, col, width)
	}
	_ = book.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})

	data, err := book.WriteToBuffer()
	if err != nil {
		http.Error(w, "File Excel gagal dibuat", http.StatusInternalServerError)
		return
	}
	filename := "data-siswa-aktif"
	if selectedClass != "" {
		filename += "-" + safeDocumentFilename(strings.ReplaceAll(selectedClass, " ", "-"))
	}
	filename += ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Content-Length", fmt.Sprint(data.Len()))
	_, _ = w.Write(data.Bytes())
}
