package handlers

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"ppdb-system/models"
	"ppdb-system/storage"
)

const passwordResetTTL = 30 * time.Minute

var resetRateLimit = struct {
	sync.Mutex
	hits map[string][]time.Time
}{hits: make(map[string][]time.Time)}

type brevoEmail struct {
	Sender      brevoContact   `json:"sender"`
	To          []brevoContact `json:"to"`
	Subject     string         `json:"subject"`
	HTMLContent string         `json:"htmlContent"`
}

type brevoContact struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// HandleForgotPassword starts a reset flow for a student account. The response
// is deliberately identical whether or not the NIK and email match an account.
func (h *AppHandler) HandleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.renderPasswordResetPage(w, "forgot_password.html", map[string]interface{}{
			"SuccessMsg": r.URL.Query().Get("success"),
			"ErrorMsg":   r.URL.Query().Get("error"),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	if !allowPasswordResetRequest(clientIP(r)) {
		h.renderPasswordResetPage(w, "forgot_password.html", map[string]interface{}{
			"ErrorMsg": "Terlalu banyak permintaan. Silakan coba kembali beberapa saat lagi.",
		})
		return
	}
	if !brevoConfigured() {
		h.renderPasswordResetPage(w, "forgot_password.html", map[string]interface{}{
			"ErrorMsg": "Layanan email pemulihan belum diaktifkan. Silakan hubungi operator sekolah.",
		})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Permintaan tidak valid", http.StatusBadRequest)
		return
	}
	nik := strings.TrimSpace(r.FormValue("nik"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	// Always show the same confirmation to avoid revealing which accounts exist.
	if nik != "" && email != "" {
		var user models.User
		if err := storage.GlobalStore.DB.Where("nik = ? AND lower(email) = ? AND role = ?", nik, email, "student").First(&user).Error; err == nil {
			rawToken := make([]byte, 32)
			if _, err := cryptorand.Read(rawToken); err == nil {
				token := hex.EncodeToString(rawToken)
				hash := hashResetToken(token)
				expires := time.Now().Add(passwordResetTTL)
				// Replace any earlier token so only the newest link remains valid.
				if err := storage.GlobalStore.DB.Where("user_id = ?", user.ID).Delete(&models.PasswordResetToken{}).Error; err == nil {
					reset := models.PasswordResetToken{TokenHash: hash, UserID: user.ID, ExpiresAt: expires, CreatedAt: time.Now()}
					if err := storage.GlobalStore.DB.Create(&reset).Error; err == nil {
						if link, err := passwordResetURL(token); err == nil {
							if err := sendBrevoResetEmail(user.Email, user.FullName, link); err != nil {
								log.Printf("password reset email delivery failed: %v", err)
								_ = storage.GlobalStore.DB.Delete(&reset).Error
							}
						} else {
							_ = storage.GlobalStore.DB.Delete(&reset).Error
						}
					}
				}
			}
		}
	}
	http.Redirect(w, r, "/lupa-password?success="+url.QueryEscape("Jika NIK dan email cocok dengan akun siswa, tautan pemulihan akan dikirim ke email terdaftar."), http.StatusSeeOther)
}

// HandleResetPassword validates and consumes a single-use reset token.
func (h *AppHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Permintaan tidak valid", http.StatusBadRequest)
			return
		}
		token = strings.TrimSpace(r.FormValue("token"))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")
		if len([]byte(password)) < 8 || len([]byte(password)) > 72 {
			h.renderPasswordResetPage(w, "reset_password.html", map[string]interface{}{"Token": token, "ErrorMsg": "Kata sandi harus terdiri dari 8–72 byte."})
			return
		}
		if password != confirm {
			h.renderPasswordResetPage(w, "reset_password.html", map[string]interface{}{"Token": token, "ErrorMsg": "Konfirmasi kata sandi tidak sama."})
			return
		}
		hash := hashResetToken(token)
		err := storage.GlobalStore.DB.Transaction(func(tx *gorm.DB) error {
			var reset models.PasswordResetToken
			if err := tx.Where("token_hash = ? AND expires_at > ?", hash, time.Now()).First(&reset).Error; err != nil {
				return err
			}
			var user models.User
			if err := tx.Where("id = ? AND role = ?", reset.UserID, "student").First(&user).Error; err != nil {
				return err
			}
			result := tx.Model(&user).Updates(map[string]interface{}{
				"password_hash":       storage.HashPassword(password),
				"password_ciphertext": "",
				"updated_at":          time.Now(),
			})
			if result.Error != nil {
				return result.Error
			}
			return tx.Delete(&reset).Error
		})
		if err != nil {
			_ = storage.GlobalStore.DB.Where("token_hash = ? AND expires_at <= ?", hash, time.Now()).Delete(&models.PasswordResetToken{}).Error
			h.renderPasswordResetPage(w, "reset_password.html", map[string]interface{}{"Token": token, "ErrorMsg": "Tautan tidak valid atau sudah kedaluwarsa. Minta tautan pemulihan baru."})
			return
		}
		http.Redirect(w, r, "/login?success="+url.QueryEscape("Kata sandi berhasil diubah. Silakan masuk dengan kata sandi baru."), http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Metode tidak diizinkan", http.StatusMethodNotAllowed)
		return
	}
	var reset models.PasswordResetToken
	if token == "" || storage.GlobalStore.DB.Where("token_hash = ? AND expires_at > ?", hashResetToken(token), time.Now()).First(&reset).Error != nil {
		h.renderPasswordResetPage(w, "reset_password.html", map[string]interface{}{"ErrorMsg": "Tautan tidak valid atau sudah kedaluwarsa. Minta tautan pemulihan baru."})
		return
	}
	h.renderPasswordResetPage(w, "reset_password.html", map[string]interface{}{"Token": token})
}

func (h *AppHandler) renderPasswordResetPage(w http.ResponseWriter, name string, data map[string]interface{}) {
	page, err := template.ParseFiles(h.tmplDir + "/" + name)
	if err != nil {
		http.Error(w, "Halaman tidak dapat dimuat", http.StatusInternalServerError)
		return
	}
	if err := page.Execute(w, data); err != nil {
		http.Error(w, "Halaman tidak dapat ditampilkan", http.StatusInternalServerError)
	}
}

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func brevoConfigured() bool {
	return strings.TrimSpace(os.Getenv("BREVO_API_KEY")) != "" && strings.TrimSpace(os.Getenv("BREVO_SENDER_EMAIL")) != "" && strings.TrimSpace(resetBaseURL()) != ""
}

func passwordResetURL(token string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(resetBaseURL()), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && os.Getenv("APP_ENV") != "production")) {
		return "", fmt.Errorf("APP_BASE_URL must be a valid HTTPS URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/reset-password"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func resetBaseURL() string {
	if base := strings.TrimSpace(os.Getenv("APP_BASE_URL")); base != "" {
		return base
	}
	if domain := strings.TrimSpace(os.Getenv("RAILWAY_PUBLIC_DOMAIN")); domain != "" {
		if strings.Contains(domain, "://") {
			return domain
		}
		return "https://" + domain
	}
	if os.Getenv("APP_ENV") != "production" {
		return "http://localhost:8080"
	}
	return ""
}

func sendBrevoResetEmail(to, name, link string) error {
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("recipient email is empty")
	}
	fromName := strings.TrimSpace(os.Getenv("BREVO_SENDER_NAME"))
	if fromName == "" {
		fromName = "RA Tunas Cendekia"
	}
	payload := brevoEmail{
		Sender:      brevoContact{Email: strings.TrimSpace(os.Getenv("BREVO_SENDER_EMAIL")), Name: fromName},
		To:          []brevoContact{{Email: to, Name: name}},
		Subject:     "Tautan Pemulihan Kata Sandi PPDB",
		HTMLContent: fmt.Sprintf(`<div style="font-family:Arial,sans-serif;max-width:560px;margin:auto;color:#1e293b"><h2>Pemulihan Kata Sandi</h2><p>Halo %s,</p><p>Kami menerima permintaan untuk mengatur ulang kata sandi akun PPDB Anda.</p><p><a href="%s" style="display:inline-block;background:#047857;color:#fff;padding:12px 18px;border-radius:8px;text-decoration:none">Atur Ulang Kata Sandi</a></p><p>Tautan berlaku selama 30 menit dan hanya dapat digunakan satu kali. Jika Anda tidak meminta penggantian kata sandi, abaikan email ini.</p></div>`, template.HTMLEscapeString(name), template.HTMLEscapeString(link)),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", strings.TrimSpace(os.Getenv("BREVO_API_KEY")))
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Brevo returned status %d", resp.StatusCode)
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func allowPasswordResetRequest(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-15 * time.Minute)
	resetRateLimit.Lock()
	defer resetRateLimit.Unlock()
	hits := resetRateLimit.hits[ip]
	active := hits[:0]
	for _, at := range hits {
		if at.After(cutoff) {
			active = append(active, at)
		}
	}
	if len(active) >= 5 {
		resetRateLimit.hits[ip] = active
		return false
	}
	resetRateLimit.hits[ip] = append(active, now)
	return true
}
