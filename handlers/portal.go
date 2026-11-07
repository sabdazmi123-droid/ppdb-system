package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"ppdb-system/models"
	"ppdb-system/storage"
)

func (h *AppHandler) HandlePublicPortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var settings models.PortalSettings
	if err := storage.GlobalStore.DB.First(&settings, 1).Error; err != nil {
		http.Error(w, "Konten portal belum tersedia", http.StatusInternalServerError)
		return
	}
	var items []models.PortalItem
	if err := storage.GlobalStore.DB.Where("active = ?", true).Order("sort_order asc, created_at desc").Find(&items).Error; err != nil {
		http.Error(w, "Konten portal gagal dimuat", http.StatusInternalServerError)
		return
	}
	services, infos, _, _ := groupPortalItems(items)
	var profile, visionMission, learning []models.PortalItem
	for _, item := range infos {
		switch strings.ToLower(strings.TrimSpace(item.Category)) {
		case "profil":
			profile = append(profile, item)
		case "visi misi", "visi-misi", "visimisi":
			visionMission = append(visionMission, item)
		case "sistem pembelajaran", "pembelajaran":
			learning = append(learning, item)
		}
	}
	school := storage.GlobalStore.GetSchool()
	filled, remaining, percent, configured, full := admissionQuotaStatus(school)
	registrationAvailable := school.RegistrationOpen && !full
	currentUser := h.GetCurrentUser(r)
	canEdit := currentUser != nil && currentUser.Role != "student"
	page, err := template.ParseFiles(filepath.Join(h.tmplDir, "portal_home.html"))
	if err != nil {
		log.Printf("portal home template parse failed: %v", err)
		http.Error(w, "Halaman portal gagal dimuat", http.StatusInternalServerError)
		return
	}
	data := map[string]interface{}{
		"School": school, "Settings": settings,
		"Services": services, "Profile": profile, "VisionMission": visionMission, "Learning": learning,
		"IsAdmin":     canEdit,
		"QuotaFilled": filled, "QuotaRemaining": remaining, "QuotaPercent": percent,
		"QuotaConfigured": configured, "QuotaFull": full, "RegistrationAvailable": registrationAvailable,
	}
	if err := page.Execute(w, data); err != nil {
		log.Printf("portal home template render failed: %v", err)
		http.Error(w, "Halaman portal gagal ditampilkan", http.StatusInternalServerError)
	}
}

func groupPortalItems(items []models.PortalItem) (services, infos, media, news []models.PortalItem) {
	for _, item := range items {
		switch item.Kind {
		case "service":
			services = append(services, item)
		case "info":
			infos = append(infos, item)
		case "media":
			media = append(media, item)
		case "news":
			news = append(news, item)
		}
	}
	return
}

func (h *AppHandler) HandlePortalNews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	var item models.PortalItem
	if err := storage.GlobalStore.DB.Where("id = ? AND kind = ? AND active = ?", r.URL.Query().Get("id"), "news", true).First(&item).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := template.ParseFiles(filepath.Join(h.tmplDir, "portal_news_detail.html"))
	if err != nil {
		http.Error(w, "Halaman berita gagal dimuat", http.StatusInternalServerError)
		return
	}
	if err := page.Execute(w, map[string]interface{}{"School": storage.GlobalStore.GetSchool(), "Item": item}); err != nil {
		http.Error(w, "Berita gagal ditampilkan", http.StatusInternalServerError)
	}
}

func (h *AppHandler) HandlePortalMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	var item models.PortalItem
	if err := storage.GlobalStore.DB.Where("id = ? AND kind = ? AND active = ?", r.URL.Query().Get("id"), "media", true).First(&item).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := template.ParseFiles(filepath.Join(h.tmplDir, "portal_media_detail.html"))
	if err != nil {
		log.Printf("portal media detail template parse failed: %v", err)
		http.Error(w, "Halaman media gagal dimuat", http.StatusInternalServerError)
		return
	}
	if err := page.Execute(w, map[string]interface{}{"School": storage.GlobalStore.GetSchool(), "Item": item}); err != nil {
		log.Printf("portal media detail template render failed: %v", err)
		http.Error(w, "Media gagal ditampilkan", http.StatusInternalServerError)
	}
}

func (h *AppHandler) HandlePortalCMS(w http.ResponseWriter, r *http.Request) {
	user := h.GetCurrentUser(r)
	if user == nil || user.Role == "student" {
		http.Error(w, "Akses hanya untuk operator", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		h.handlePortalCMSPost(w, r, user)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	var settings models.PortalSettings
	if err := storage.GlobalStore.DB.First(&settings, 1).Error; err != nil {
		http.Error(w, "Pengaturan portal gagal dimuat", http.StatusInternalServerError)
		return
	}
	var items []models.PortalItem
	if err := storage.GlobalStore.DB.Order("kind asc, sort_order asc, created_at desc").Find(&items).Error; err != nil {
		http.Error(w, "Konten portal gagal dimuat", http.StatusInternalServerError)
		return
	}
	var revisions []models.PortalRevision
	if err := storage.GlobalStore.DB.Order("created_at desc, id desc").Limit(50).Find(&revisions).Error; err != nil {
		http.Error(w, "Riwayat CMS gagal dimuat", http.StatusInternalServerError)
		return
	}
	page, err := template.New("portal_cms.html").Funcs(template.FuncMap{
		"eq": func(a, b string) bool { return a == b },
		"actionLabel": func(action string) string {
			switch action {
			case "create":
				return "Konten dibuat"
			case "update":
				return "Konten diperbarui"
			case "delete":
				return "Konten dihapus"
			case "settings":
				return "Tampilan utama diperbarui"
			default:
				return action
			}
		},
		"publicAnchor": func(kind string) string {
			switch kind {
			case "info":
				return "profil"
			case "media":
				return "media"
			case "news":
				return "berita"
			default:
				return "layanan"
			}
		},
		"previewURL": func(kind, id, category string) string {
			switch kind {
			case "media":
				return "/media?id=" + url.QueryEscape(id)
			case "news":
				return "/berita?id=" + url.QueryEscape(id)
			case "info":
				switch strings.ToLower(strings.TrimSpace(category)) {
				case "visi-misi", "visi misi", "visimisi":
					return "/#visi-misi"
				case "pembelajaran", "sistem pembelajaran":
					return "/#pembelajaran"
				default:
					return "/#profil"
				}
			default:
				return "/#layanan"
			}
		},
	}).ParseFiles(filepath.Join(h.tmplDir, "portal_cms.html"))
	if err != nil {
		http.Error(w, "Halaman CMS gagal dimuat", http.StatusInternalServerError)
		return
	}
	data := map[string]interface{}{
		"Settings": settings, "Items": items, "Revisions": revisions,
		"SuccessMsg": r.URL.Query().Get("success"), "ErrorMsg": r.URL.Query().Get("error"),
	}
	if err := page.Execute(w, data); err != nil {
		http.Error(w, "Halaman CMS gagal ditampilkan", http.StatusInternalServerError)
	}
}

func (h *AppHandler) handlePortalCMSPost(w http.ResponseWriter, r *http.Request, actor *models.User) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(10 << 20)
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape("Form terlalu besar atau tidak valid"), http.StatusSeeOther)
		return
	}
	message := "Perubahan konten berhasil disimpan"
	switch r.FormValue("action") {
	case "settings":
		var settings models.PortalSettings
		if err := storage.GlobalStore.DB.First(&settings, 1).Error; err != nil {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape("Pengaturan portal tidak ditemukan"), http.StatusSeeOther)
			return
		}
		settings.HeroTitle = strings.TrimSpace(r.FormValue("hero_title"))
		settings.HeroDescription = strings.TrimSpace(r.FormValue("hero_description"))
		settings.HeroImage = strings.TrimSpace(r.FormValue("hero_image"))
		settings.Announcement = strings.TrimSpace(r.FormValue("announcement"))
		settings.AnnouncementURL = strings.TrimSpace(r.FormValue("announcement_url"))
		settings.ContactText = strings.TrimSpace(r.FormValue("contact_text"))
		settings.ContactURL = strings.TrimSpace(r.FormValue("contact_url"))
		if settings.HeroTitle == "" || !validPortalURL(settings.HeroImage) || !validPortalURL(settings.AnnouncementURL) || !validPortalURL(settings.ContactURL) {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape("Judul utama wajib diisi dan tautan harus menggunakan URL http(s) atau path lokal"), http.StatusSeeOther)
			return
		}
		if image, err := h.savePortalImage(r, "hero_upload", "hero"); err != nil {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		} else if image != "" {
			settings.HeroImage = image
		}
		settings.UpdatedAt = time.Now()
		if err := storage.GlobalStore.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(&settings).Error; err != nil {
				return err
			}
			return createPortalRevision(tx, actor, "settings", "portal-settings", settings.HeroTitle, "settings", settings)
		}); err != nil {
			message = "Gagal menyimpan pengaturan portal"
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape(message+": "+err.Error()), http.StatusSeeOther)
			return
		}
	case "save_item":
		isNew := strings.TrimSpace(r.FormValue("id")) == ""
		item, err := h.portalItemFromForm(r)
		if err != nil {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		if image, err := h.savePortalImage(r, "image_upload", item.ID); err != nil {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		} else if image != "" {
			item.ImageURL = image
		}
		action := "update"
		if isNew {
			action = "create"
		}
		if err := storage.GlobalStore.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(&item).Error; err != nil {
				return err
			}
			return createPortalRevision(tx, actor, item.Kind, item.ID, item.Title, action, item)
		}); err != nil {
			message = "Gagal menyimpan konten: " + err.Error()
		}
	case "delete_item":
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "portal-ppdb" {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape("Kartu PPDB utama tidak dapat dihapus"), http.StatusSeeOther)
			return
		}
		var item models.PortalItem
		if err := storage.GlobalStore.DB.First(&item, "id = ?", id).Error; err != nil {
			http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape("Konten tidak ditemukan"), http.StatusSeeOther)
			return
		}
		if err := storage.GlobalStore.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&models.PortalItem{}, "id = ?", id).Error; err != nil {
				return err
			}
			return createPortalRevision(tx, actor, item.Kind, item.ID, item.Title, "delete", item)
		}); err != nil {
			message = "Gagal menghapus konten: " + err.Error()
		} else {
			message = "Konten berhasil dihapus"
		}
	default:
		message = "Aksi CMS tidak dikenal"
	}
	if strings.HasPrefix(message, "Gagal") || message == "Aksi CMS tidak dikenal" {
		http.Redirect(w, r, "/admin/cms?error="+url.QueryEscape(message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/cms?success="+url.QueryEscape(message), http.StatusSeeOther)
}

func createPortalRevision(tx *gorm.DB, actor *models.User, entityType, entityID, title, action string, snapshot interface{}) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	actorName := "Operator"
	if actor != nil {
		actorName = strings.TrimSpace(actor.FullName)
		if actorName == "" {
			actorName = actor.NIK
		}
	}
	return tx.Create(&models.PortalRevision{
		EntityType: entityType, EntityID: entityID, Title: title,
		Action: action, Actor: actorName, Snapshot: string(data), CreatedAt: time.Now(),
	}).Error
}

func (h *AppHandler) portalItemFromForm(r *http.Request) (models.PortalItem, error) {
	id := strings.TrimSpace(r.FormValue("id"))
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind != "service" && kind != "info" && kind != "media" && kind != "news" {
		return models.PortalItem{}, fmt.Errorf("jenis konten tidak valid")
	}
	item := models.PortalItem{ID: id}
	if id != "" {
		if err := storage.GlobalStore.DB.First(&item, "id = ?", id).Error; err != nil {
			return models.PortalItem{}, fmt.Errorf("konten tidak ditemukan")
		}
	} else {
		item.ID = fmt.Sprintf("CMS-%d", time.Now().UnixNano())
		item.CreatedAt = time.Now()
		publishedAt := time.Now()
		item.PublishedAt = &publishedAt
	}
	item.Kind = kind
	item.Title = strings.TrimSpace(r.FormValue("title"))
	item.Subtitle = strings.TrimSpace(r.FormValue("subtitle"))
	item.Body = strings.TrimSpace(r.FormValue("body"))
	item.Icon = strings.TrimSpace(r.FormValue("icon"))
	item.URL = strings.TrimSpace(r.FormValue("url"))
	item.ImageURL = strings.TrimSpace(r.FormValue("image_url"))
	item.Category = strings.TrimSpace(r.FormValue("category"))
	item.Active = r.FormValue("active") == "on"
	if item.ID == "portal-ppdb" {
		item.Active = true
	}
	if order := strings.TrimSpace(r.FormValue("sort_order")); order != "" {
		if _, err := fmt.Sscanf(order, "%d", &item.SortOrder); err != nil {
			return models.PortalItem{}, fmt.Errorf("urutan konten harus berupa angka")
		}
	}
	if item.Title == "" || !validPortalURL(item.URL) || !validPortalURL(item.ImageURL) {
		return models.PortalItem{}, fmt.Errorf("judul wajib diisi dan tautan harus menggunakan URL http(s) atau path lokal")
	}
	if kind == "service" && item.Icon == "" {
		item.Icon = "fa-solid fa-circle-arrow-right"
	}
	item.UpdatedAt = time.Now()
	return item, nil
}

func validPortalURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, "/") {
		return !strings.HasPrefix(value, "//")
	}
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http")
}

func (h *AppHandler) savePortalImage(r *http.Request, field, fileKey string) (string, error) {
	file, header, err := r.FormFile(field)
	if err == http.ErrMissingFile {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("gambar tidak dapat dibaca")
	}
	defer file.Close()
	if header.Size > 5<<20 {
		return "", fmt.Errorf("ukuran gambar maksimal 5 MB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 5<<20 {
		return "", fmt.Errorf("gambar kosong atau melebihi 5 MB")
	}
	contentType := http.DetectContentType(data)
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}[strings.Split(contentType, ";")[0]]
	if ext == "" {
		return "", fmt.Errorf("format gambar harus JPG, PNG, atau WEBP")
	}
	if err := os.MkdirAll(h.uploadDir, 0755); err != nil {
		return "", fmt.Errorf("folder unggahan tidak tersedia")
	}
	name := fmt.Sprintf("cms-%s-%d%s", fileKey, time.Now().UnixNano(), ext)
	path := filepath.Join(h.uploadDir, filepath.Base(name))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("gambar gagal disimpan")
	}
	return "/static/uploads/" + name, nil
}
