package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ppdb-system/handlers"
	"ppdb-system/storage"
)

func main() {
	if os.Getenv("APP_ENV") == "production" && len(os.Getenv("PPDB_SESSION_SECRET")) < 32 {
		log.Fatal("PPDB_SESSION_SECRET wajib diisi minimal 32 karakter pada produksi")
	}
	// Direktori kerja
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	dataDir := os.Getenv("PPDB_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(cwd, "data")
	}
	tmplDir := filepath.Join(cwd, "templates")
	staticDir := filepath.Join(cwd, "static")
	uploadDir := os.Getenv("PPDB_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = filepath.Join(staticDir, "uploads")
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Fatalf("Gagal membuat direktori upload: %v", err)
	}

	// 1. Inisialisasi MySQL (jika MYSQL_URL/DATABASE_URL tersedia), atau SQLite lokal.
	store, err := storage.InitStore(dataDir)
	if err != nil {
		log.Fatalf("Gagal inisialisasi database: %v", err)
	}
	_ = store

	// 2. Inisialisasi Handler Controller
	h := handlers.NewAppHandler(tmplDir, uploadDir)

	// 3. Routing HTTP Server
	mux := http.NewServeMux()

	// Static Assets
	fileServer := http.FileServer(http.Dir(staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
	if os.Getenv("PPDB_UPLOAD_DIR") != "" {
		uploadServer := http.FileServer(http.Dir(uploadDir))
		mux.Handle("/static/uploads/", http.StripPrefix("/static/uploads/", uploadServer))
	}

	// Auth & Akun
	mux.HandleFunc("/login", h.HandleLogin)
	mux.HandleFunc("/lupa-password", h.HandleForgotPassword)
	mux.HandleFunc("/reset-password", h.HandleResetPassword)
	mux.HandleFunc("/admin/cms", h.HandlePortalCMS)
	mux.HandleFunc("/admin/pengaturan-dokumen", h.HandleDocumentSettings)
	mux.HandleFunc("/berita", h.HandlePortalNews)
	mux.HandleFunc("/media", h.HandlePortalMedia)
	mux.HandleFunc("/register", h.HandleRegister)
	mux.HandleFunc("/pengaturan-ppdb", h.HandlePPDBSettings)
	mux.HandleFunc("/logout", h.HandleLogout)
	mux.HandleFunc("/ubah-kata-sandi", h.HandleChangePassword)

	// Home & Dashboard Utama
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			h.HandlePublicPortal(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		// Jika yang login siswa, arahkan ke menu siswa
		if u := h.GetCurrentUser(r); u != nil && u.Role == "student" {
			http.Redirect(w, r, "/profile-siswa", http.StatusSeeOther)
			return
		}
		h.HandleDashboard(w, r)
	})

	// Modul 1: Generate Password untuk siswa yang formulirnya sudah disetujui
	mux.HandleFunc("/generate-password", h.HandleGeneratePassword)
	mux.HandleFunc("/generate-password/delete", h.HandleDeleteApplicant)
	mux.HandleFunc("/generate-password/document", h.HandleDownloadStudentAccountDocument)

	// Modul 2: Formulir PPDB
	mux.HandleFunc("/formulir-ppdb", h.HandleFormulirPPDB)
	mux.HandleFunc("/formulir-ppdb/approve", h.HandleApproveFormulir)
	mux.HandleFunc("/formulir-ppdb/reject", h.HandleRejectFormulir)

	// Modul 3: Siswa Diterima & EMIS
	mux.HandleFunc("/siswa-diterima", h.HandleSiswaDiterima)
	mux.HandleFunc("/siswa-diterima/emis", h.HandleUpdateEmis)
	mux.HandleFunc("/kartu-siswa", h.HandleKartuSiswa)

	// Modul 4: Data Siswa Aktif
	mux.HandleFunc("/siswa-aktif", h.HandleDataSiswaAktif)
	mux.HandleFunc("/siswa-aktif/export", h.HandleExportActiveStudents)
	mux.HandleFunc("/siswa-aktif/cetak", h.HandleCetakAbsensi)
	mux.HandleFunc("/admin/rdm-input", h.HandleAdminRDMInput)

	// Modul 5: Impor NISN EMIS
	mux.HandleFunc("/pengisian-nisn", h.HandleImportNISN)
	mux.HandleFunc("/pengisian-npsn", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pengisian-nisn", http.StatusMovedPermanently)
	})

	// Modul 6: Kenaikan Kelas & Kelulusan
	mux.HandleFunc("/kenaikan-kelulusan", h.HandleKenaikanKelulusan)
	mux.HandleFunc("/kenaikan-kelulusan/proses", h.HandleProsesKenaikan)

	// Modul 7: Data Siswa Keluar
	mux.HandleFunc("/siswa-keluar", h.HandleDataSiswaKeluar)

	// Modul 8: Data Siswa Lulus & Ijazah
	mux.HandleFunc("/siswa-lulus", h.HandleDataSiswaLulus)
	mux.HandleFunc("/siswa-lulus/import", h.HandleImportLulus)

	// ================= MENU KHUSUS USER (STUDENT) =================
	mux.HandleFunc("/profile-siswa", h.HandleProfileSiswa)
	mux.HandleFunc("/formulir/cetak", h.HandleCetakFormulir)
	mux.HandleFunc("/student/status", h.HandleStudentStatus)
	mux.HandleFunc("/student/rdm", h.HandleStudentRDM)
	mux.HandleFunc("/student/ijazah", h.HandleStudentIjazah)

	// API Integrasi RDM
	mux.HandleFunc("/api/rdm-sync", h.HandleSyncRDM)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}

	fmt.Println("=================================================================")
	dbMode := "SQLite lokal"
	if os.Getenv("MYSQL_URL") != "" || os.Getenv("DATABASE_URL") != "" || os.Getenv("MYSQL_DSN") != "" {
		dbMode = "MySQL server"
	}
	fmt.Printf("🚀 SISTEM PPDB RA TUNAS CENDEKIA (GOLANG + %s + GORM) PORT %s\n", dbMode, port)
	fmt.Printf("🌐 URL: http://localhost:%s\n", port)
	fmt.Printf("💾 Database: %s + GORM v2 ORM\n", dbMode)
	fmt.Printf("🔒 Password: bcrypt hashing (production-ready)\n")
	fmt.Println("=================================================================")
	if os.Getenv("APP_ENV") != "production" {
		fmt.Println("👉 Login Operator:  NIK: admin | Password: admin123")
		fmt.Println("👉 Login Siswa:     NIK: 3201011405190001 | Password: 123456")
	} else {
		fmt.Println("🔐 Akun operator produksi menggunakan kredensial konfigurasi server")
	}
	fmt.Println("=================================================================")

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Fatal(server.ListenAndServe())
}
