# Sistem PPDB & Kesiswaan Madrasah (Golang)

Aplikasi web terpadu **Sistem Penerimaan Peserta Didik Baru (PPDB) & Manajemen Kesiswaan Madrasah** berbasis bahasa pemrograman **Golang**, dirancang secara khusus mengacu pada desain antarmuka dashboard Kemenag/Madrasah serta spesifikasi alur operasional madrasah (RA / MTs / MA / SMK / SMA).

---

## 🌟 Fitur Utama & Kesesuaian Spesifikasi

1. **Dashboard Utama (Sesuai Rancangan Desain Gambar WhatsApp):**
   - **Header & Profil:** Menampilkan jenjang MTs / MA / SMK / SMA / RA, Jam & Hari WIB realtime, Notifikasi lonceng, dan profil akun Admin Sekolah / Operator.
   - **Banner Sambutan:** Teks islami *"Assalamu'alaikum, Selamat Datang di Sistem PPDB"* dilengkapi banner gedung dan slogan *"Pendidikan Merdeka Masa Depan Gemilang"*.
   - **5 Card Statistik Utama:**
     - **Total Pendaftar** (248) - Tren &uarr; 12%
     - **Siswa Diterima** (180) - Tren &uarr; 15%
     - **Siswa Aktif** (165) - Tren &uarr; 10%
     - **Siswa Keluar** (15) - Tren &darr; 6%
     - **Siswa Lulus** (148) - Tren &uarr; 18%
   - **Grafik Pendaftar dan Siswa:** Bar chart bulanan (Jun s/d Des) dengan filter Tahun Ajaran (Chart.js).
   - **Status Siswa:** Donut chart persentase kelulusan, penerimaan, keaktifan, dan mutasi.
   - **Aktivitas Terbaru & Notifikasi:** Rekapitulasi feed realtime aktivitas sistem.

2. **Pendaftaran Calon Siswa:**
   - Calon siswa/orang tua mengisi satu form ringkas berisi identitas, kontak, dan tahun ajaran; tidak perlu secret key atau membuat password.
   - Pengajuan masuk ke menu verifikasi admin dengan status menunggu.

3. **Pemeriksaan & Pembuatan Akun Siswa:**
   - Admin memeriksa data di menu Formulir PPDB dan menyetujui pendaftar.
   - Pendaftar tampil di **Generate Password** dan **Formulir PPDB**. Admin dapat membuat akun dengan password pilihan sendiri atau password acak; status penerimaan tetap dikelola dari Formulir PPDB. Setelah akun dibuat, admin dapat mengunduh surat akun Word dari template sekolah, yang memuat nomor pendaftaran, tahun ajaran, NIK, nama siswa, dan password.

4. **Login Multi-Role:**
   - Menggunakan **NIK** dan **Password**.
   - Akun Siswa langsung diarahkan ke form & profil siswa.
   - Akun Operator/Admin diarahkan ke Dashboard Manajemen.

5. **Formulir PPDB (50 Field Standar Kemenag):**
   - Berisi 50 pertanyaan lengkap (Identitas, Orang Tua/Wali, Tempat Tinggal & Domisili, Riwayat Sekolah PAUD/TK, Imunisasi Lengkap, Bantuan KKS/PKH/KIP, dan Konfirmasi Persetujuan).
   - **Alur Verifikasi:**
     - **Approve:** Otomatis menghasilkan **NIS Lokal** dengan format: `NSM + Tahun Masuk + 4 Digit Nomor Urut` (contoh: `10123201008820250005`), menempatkan siswa ke kelas awal (misal Kelas A), dan status masuk ke **Siswa Diterima**.
     - **Perbaiki / Tolak:** Admin menyertakan catatan revisi kekurangan berkas. Data siswa otomatis **berubah warna merah** pada tabel dan formulir untuk menarik perhatian bahwa data perlu perbaikan.

6. **Siswa Diterima & Alur EMIS:**
   - Proses pelaporan ke sistem EMIS Kemenag dengan 3 tombol aksi: **Di Input**, **Perbaiki**, dan **Aprove**.
   - Jika sudah di-**Aprove**, sistem otomatis menerbitkan **Kartu Tanda Siswa (KTS)** resmi.
   - **Kartu Tanda Siswa (KTS):** Format ID card standar Kemenag dilengkapi Foto Siswa, Nama Madrasah, NPSN, Nama Siswa, NISN, NIS Lokal, Jenis Kelamin, Kelas, Tempat Tanggal Lahir, Barcode, QR Code, dan tanda tangan/stempel Kepala Madrasah.

7. **Data Siswa Aktif:**
   - Pengelompokan Rombel: **Kelas A**, **Kelas B1**, dan **Kelas B2**.
   - Menampilkan informasi Tahun Pelajaran, Nama Guru Pengajar, dan Wali Kelas pengampu.
   - Fitur cetak absensi dan sinkronisasi ke Raport Digital Madrasah (RDM).

8. **Pengisian NPSN (Profil Lembaga):**
   - Konfigurasi NPSN, NSM, Nama Lembaga, Jenjang (RA/MTs/MA/SMK/SMA), Akreditasi, Alamat, No Telepon, Email, dan Kepala Madrasah beserta NIP.

9. **Kenaikan Kelas dan Kelulusan:**
   - **Naikkan Kelas:** Muncul pilihan kelas tujuan (Kelas B1 / Kelas B2) &rarr; data otomatis masuk ke Siswa Aktif kelas baru.
   - **Mengulang Kelas:** Muncul pilihan kelas (Kelas A / B1 / B2) &rarr; data tetap berada di Siswa Aktif dengan keterangan mengulang.
   - **Pindah Madrasah:** Muncul form isian nama madrasah/sekolah tujuan dan alasan pindah &rarr; data otomatis masuk ke **Data Siswa Keluar**.
   - **Siswa Lulus:** Muncul input nomor seri ijazah dan upload hasil scan ijazah &rarr; data otomatis masuk ke **Data Siswa Lulus**.

10. **Data Siswa Keluar:**
    - Filter dropdown **Tahun Pelajaran** di bagian atas tabel.
    - Rekapitulasi mutasi dan cetak surat pindah madrasah.

11. **Data Siswa Lulus & Arsip Ijazah:**
    - Filter dropdown **Tahun Kelulusan** di bagian atas tabel.
    - Tombol **Import Siswa Lulus Tahun Sebelumnya** untuk menginput rekapitulasi data alumni lampau.
    - Menampilkan Nomor Seri Ijazah dan tautan preview dokumen hasil scan ijazah.

12. **Profile Siswa:**
    - 3 Tombol Aksi: **Cetak Formulir**, **Perbaiki Data**, dan **Simpan & Kirim**.

---

## 📁 Struktur Direktori

```
ppdb-system/
├── main.go                     # Entry point HTTP server Golang
├── models/
│   └── models.go               # Struct data model & 50 field formulir
├── storage/
│   └── storage.go              # Storage engine persistence & business logic
├── handlers/
│   └── handlers.go             # Controller handler & routing logic
├── templates/                  # Template HTML (Tailwind CSS & Chart.js)
│   ├── layout.html             # Layout utama dengan sidebar hijau Kemenag
│   ├── dashboard.html          # Dashboard persis rancangan desain gambar
│   ├── generate_password.html  # Daftar pendaftar disetujui tanpa akun
│   ├── formulir_ppdb.html      # Verifikasi formulir (Approve & Perbaiki merah)
│   ├── siswa_diterima.html     # Alur EMIS & Penerbitan KTS
│   ├── kartu_siswa_print.html  # Desain cetak KTS resmi
│   ├── siswa_aktif.html        # Rombel Kelas A, B1, B2 & Wali Kelas
│   ├── pengisian_npsn.html     # Pengisian data NPSN & profil madrasah
│   ├── kenaikan_kelulusan.html # 4 Modal aksi kenaikan/kelulusan
│   ├── siswa_keluar.html       # Rekapitulasi mutasi keluar per tahun
│   ├── siswa_lulus.html        # Kelulusan, nomor ijazah & import alumni
│   ├── profile_siswa.html      # Profil lengkap 50 pertanyaan formulir
│   ├── cetak_formulir_print.html # Cetak formulir resmi A4/F4
│   ├── login.html              # Login NIK & Password
│   └── register.html           # Form pendaftaran calon siswa satu halaman
├── static/                     # Asset statis, upload scan ijazah & KTS
└── data/
    └── ppdb_data.json          # File database persisten otomatis
```

---

## 🚀 Cara Menjalankan Aplikasi

Aplikasi telah berhasil dikompilasi menjadi binary mandiri `ppdb-system.exe`.

1. Buka PowerShell atau Command Prompt pada direktori proyek:
   ```powershell
   cd "C:\Users\Tunas Cendekia\.gemini\antigravity\scratch\ppdb-system"
   ```

2. Jalankan aplikasi:
   ```powershell
   .\ppdb-system.exe
   ```
   *(Atau jalankan dari source code dengan perintah: `go run .`)*

3. Buka browser dan akses alamat:
   **http://localhost:8085**

---

## 🗄️ Konfigurasi Database MySQL di Railway

Aplikasi memakai SQLite untuk lokal jika variabel koneksi MySQL tidak diatur. Di Railway, tambahkan service MySQL lalu set `MYSQL_URL` pada service aplikasi ke reference variable URL MySQL dari service tersebut (umumnya `${{MySQL.MYSQL_URL}}`). Aplikasi akan membuat/memperbarui tabel secara otomatis saat mulai.

`MYSQL_URL` menerima URL `mysql://user:password@host:port/database`; `MYSQL_DSN` menerima DSN native Go MySQL. Data SQLite lokal tidak disalin otomatis ke MySQL, jadi ekspor/impor database perlu dilakukan sebelum mengganti deployment yang sudah berisi data. Simpan upload persisten dengan volume yang dipasang pada `/data`.

Tanpa URL MySQL, aplikasi tetap memakai `data/ppdb.db` untuk pengembangan lokal.

---

## 🔑 Akun Uji Coba Default

| Peran | NIK / Username | Password | Deskripsi |
|---|---|---|---|
| **Admin / Operator Madrasah** | `admin` | `admin123` | Akses penuh dashboard, verifikasi pendaftar, EMIS, kenaikan kelas, dan pembuatan password siswa. |
| **Siswa 1 (Baru Mendaftar)** | `3201011405190001` | `123456` | Siswa bernama Ahmad Fauzi (status menunggu verifikasi). |
| **Siswa 2 (Perlu Perbaikan)** | `3201012008190002` | `123456` | Siswa bernama Rizky Maulana (status berwarna merah butuh perbaikan). |
| **Siswa 3 (Diterima / KTS Terbit)** | `3201011003190003` | `123456` | Siswi bernama Siti Nurhaliza (KTS siap cetak). |
