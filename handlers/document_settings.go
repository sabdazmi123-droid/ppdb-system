package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"ppdb-system/models"
	"ppdb-system/storage"
)

func documentSettings() models.PortalSettings {
	var settings models.PortalSettings
	_ = storage.GlobalStore.DB.First(&settings, 1).Error
	return settings
}

func (h *AppHandler) HandleDocumentSettings(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil || user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	var settings models.PortalSettings
	if err := storage.GlobalStore.DB.FirstOrCreate(&settings, models.PortalSettings{ID: 1}).Error; err != nil {
		http.Error(w, "Pengaturan dokumen gagal dimuat", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Form pengaturan tidak valid", http.StatusBadRequest)
			return
		}
		style := strings.TrimSpace(r.FormValue("document_style"))
		if style != "minimal" {
			style = "formal"
		}
		values := map[string]string{
			"document_header":   strings.TrimSpace(r.FormValue("document_header")),
			"form_pdf_title":    strings.TrimSpace(r.FormValue("form_pdf_title")),
			"account_pdf_title": strings.TrimSpace(r.FormValue("account_pdf_title")),
			"document_footer":   strings.TrimSpace(r.FormValue("document_footer")),
			"document_style":    style,
		}
		if len([]rune(values["document_header"])) > 240 || len([]rune(values["form_pdf_title"])) > 240 || len([]rune(values["account_pdf_title"])) > 240 || len([]rune(values["document_footer"])) > 500 {
			http.Redirect(w, r, "/admin/pengaturan-dokumen?error="+url.QueryEscape("Panjang teks pengaturan melebihi batas"), http.StatusSeeOther)
			return
		}
		if err := storage.GlobalStore.DB.Model(&settings).Updates(values).Error; err != nil {
			http.Redirect(w, r, "/admin/pengaturan-dokumen?error="+url.QueryEscape("Pengaturan dokumen gagal disimpan"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/pengaturan-dokumen?success="+url.QueryEscape("Pengaturan template formulir dan surat akun berhasil disimpan"), http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	h.Render(w, r, "document_settings", map[string]interface{}{
		"Settings": settings, "SuccessMsg": r.URL.Query().Get("success"), "ErrorMsg": r.URL.Query().Get("error"),
	})
}
