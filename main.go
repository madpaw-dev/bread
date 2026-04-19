package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const sessionTTL = 8 * time.Hour

var (
	adminLogin    string
	adminPassword string
)

var (
	db       *sql.DB
	sessions = struct {
		mu   sync.Mutex
		data map[string]time.Time
	}{data: make(map[string]time.Time)}

	tmplOrder *template.Template
	tmplAdmin *template.Template
	tmplLogin *template.Template
)

type Order struct {
	ID           int
	Name         string
	Phone        string
	Telegram     string
	Whatsapp     string
	MaxContact   string
	PickupMethod string
	OrderDate    string
	Notes        string
	Delivered    bool
	CreatedAt    string
}

type MenuEntry struct {
	Date   string `json:"date"`
	Text   string `json:"text"`
	Hidden bool   `json:"hidden"`
}

func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "orders.db"
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite", dbPath())
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS orders (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT,
			phone         TEXT,
			telegram      TEXT,
			whatsapp      TEXT,
			max_contact   TEXT,
			pickup_method TEXT,
			order_date    TEXT,
			notes         TEXT,
			delivered     INTEGER NOT NULL DEFAULT 0,
			deleted       INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_menu (
			date    TEXT PRIMARY KEY,
			text    TEXT NOT NULL,
			hidden  INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		log.Fatal(err)
	}
	// migrations for existing installs
	db.Exec(`ALTER TABLE daily_menu ADD COLUMN hidden  INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE daily_menu ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE orders ADD COLUMN name    TEXT`)
	db.Exec(`ALTER TABLE orders ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`)
}

func newSession() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	sessions.mu.Lock()
	sessions.data[token] = time.Now().Add(sessionTTL)
	sessions.mu.Unlock()
	return token
}

func isValidSession(r *http.Request) bool {
	c, err := r.Cookie("session")
	if err != nil {
		return false
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	exp, ok := sessions.data[c.Value]
	if !ok || time.Now().After(exp) {
		delete(sessions.data, c.Value)
		return false
	}
	return true
}

// ── order form ────────────────────────────────────────────────────────────────

func orderFormHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	success := r.URL.Query().Get("success") == "1"
	tmplOrder.Execute(w, map[string]any{"Success": success})
}

func submitOrderHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ошибка разбора формы", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	phone := r.FormValue("phone")
	telegram := r.FormValue("telegram")
	whatsapp := r.FormValue("whatsapp")
	maxContact := r.FormValue("max_contact")
	pickup := r.FormValue("pickup_method")
	orderDate := r.FormValue("order_date")
	notes := r.FormValue("notes")

	if phone == "" && telegram == "" && whatsapp == "" && maxContact == "" {
		tmplOrder.Execute(w, map[string]any{"Error": "Укажите хотя бы один способ связи"})
		return
	}
	if orderDate == "" {
		tmplOrder.Execute(w, map[string]any{"Error": "Укажите дату заказа"})
		return
	}

	_, err := db.Exec(
		`INSERT INTO orders (name, phone, telegram, whatsapp, max_contact, pickup_method, order_date, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, phone, telegram, whatsapp, maxContact, pickup, orderDate, notes,
	)
	if err != nil {
		http.Error(w, "Ошибка при сохранении заказа", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?success=1", http.StatusSeeOther)
}

// ── public menu API ───────────────────────────────────────────────────────────

func apiMenuHandler(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	var text string
	db.QueryRow(`SELECT text FROM daily_menu WHERE date = ? AND hidden = 0 AND deleted = 0`, date).Scan(&text)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(text))
}

// ── admin ─────────────────────────────────────────────────────────────────────

func adminHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		tmplLogin.Execute(w, nil)
		return
	}

	dateFilter := r.URL.Query().Get("date")
	if dateFilter == "" {
		dateFilter = time.Now().Format("2006-01-02")
	}

	// orders for selected date
	oRows, err := db.Query(
		`SELECT id, name, phone, telegram, whatsapp, max_contact, pickup_method, order_date, notes, delivered, datetime(created_at, '+3 hours') AS created_at
		 FROM orders WHERE order_date = ? AND deleted = 0 ORDER BY created_at`,
		dateFilter,
	)
	if err != nil {
		http.Error(w, "Ошибка базы данных", http.StatusInternalServerError)
		return
	}
	defer oRows.Close()

	var orders []Order
	for oRows.Next() {
		var o Order
		var delivered int
		oRows.Scan(&o.ID, &o.Name, &o.Phone, &o.Telegram, &o.Whatsapp, &o.MaxContact,
			&o.PickupMethod, &o.OrderDate, &o.Notes, &delivered, &o.CreatedAt)
		o.Delivered = delivered == 1
		orders = append(orders, o)
	}

	// all menu entries (not deleted), sorted by date asc
	mRows, err := db.Query(
		`SELECT date, text, hidden FROM daily_menu WHERE deleted = 0 ORDER BY date ASC`,
	)
	if err != nil {
		http.Error(w, "Ошибка базы данных", http.StatusInternalServerError)
		return
	}
	defer mRows.Close()

	var menuEntries []MenuEntry
	for mRows.Next() {
		var e MenuEntry
		var hidden int
		mRows.Scan(&e.Date, &e.Text, &hidden)
		e.Hidden = hidden == 1
		menuEntries = append(menuEntries, e)
	}

	menuJSON, _ := json.Marshal(menuEntries)

	tmplAdmin.Execute(w, map[string]any{
		"Orders":      orders,
		"Date":        dateFilter,
		"MenuEntries": menuEntries,
		"MenuJSON":    template.JS(menuJSON),
	})
}

func saveMenuHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	date := r.FormValue("date")
	text := r.FormValue("menu_text")
	ordersDate := r.FormValue("orders_date")
	db.Exec(`INSERT INTO daily_menu (date, text, hidden, deleted) VALUES (?, ?, 0, 0)
		ON CONFLICT(date) DO UPDATE SET text = excluded.text, deleted = 0, hidden = CASE WHEN excluded.text = '' THEN hidden ELSE hidden END`,
		date, text)
	http.Redirect(w, r, "/admin?date="+ordersDate, http.StatusSeeOther)
}

func hideMenuHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	date := r.PathValue("date")
	db.Exec(`UPDATE daily_menu SET hidden = 1 WHERE date = ?`, date)
	http.Redirect(w, r, "/admin?date="+r.FormValue("orders_date"), http.StatusSeeOther)
}

func showMenuHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	date := r.PathValue("date")
	db.Exec(`UPDATE daily_menu SET hidden = 0 WHERE date = ?`, date)
	http.Redirect(w, r, "/admin?date="+r.FormValue("orders_date"), http.StatusSeeOther)
}

func deleteMenuHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	date := r.PathValue("date")
	db.Exec(`UPDATE daily_menu SET deleted = 1 WHERE date = ?`, date)
	http.Redirect(w, r, "/admin?date="+r.FormValue("orders_date"), http.StatusSeeOther)
}

func adminLoginHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ошибка разбора формы", http.StatusBadRequest)
		return
	}
	if r.FormValue("login") == adminLogin && r.FormValue("password") == adminPassword {
		token := newSession()
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessionTTL.Seconds()),
		})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	tmplLogin.Execute(w, map[string]any{"Error": "Неверный логин или пароль"})
}

func adminLogoutHandler(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("session")
	if err == nil {
		sessions.mu.Lock()
		delete(sessions.data, c.Value)
		sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func markDeliveredHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	db.Exec(`UPDATE orders SET delivered = 1 WHERE id = ?`, id)
	r.ParseForm()
	http.Redirect(w, r, "/admin?date="+r.FormValue("date"), http.StatusSeeOther)
}

func deleteOrderHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	db.Exec(`UPDATE orders SET deleted = 1 WHERE id = ?`, id)
	r.ParseForm()
	http.Redirect(w, r, "/admin?date="+r.FormValue("date"), http.StatusSeeOther)
}

func markUndeliveredHandler(w http.ResponseWriter, r *http.Request) {
	if !isValidSession(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	db.Exec(`UPDATE orders SET delivered = 0 WHERE id = ?`, id)
	r.ParseForm()
	http.Redirect(w, r, "/admin?date="+r.FormValue("date"), http.StatusSeeOther)
}

func main() {
	adminLogin = os.Getenv("ADMIN_LOGIN")
	adminPassword = os.Getenv("ADMIN_PASSWORD")
	if adminLogin == "" || adminPassword == "" {
		log.Fatal("ADMIN_LOGIN and ADMIN_PASSWORD environment variables must be set")
	}

	initDB()
	defer db.Close()

	var err error
	tmplOrder, err = template.ParseFiles("templates/order.html")
	if err != nil {
		log.Fatal("order template:", err)
	}
	tmplAdmin, err = template.ParseFiles("templates/admin.html")
	if err != nil {
		log.Fatal("admin template:", err)
	}
	tmplLogin, err = template.ParseFiles("templates/login.html")
	if err != nil {
		log.Fatal("login template:", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", orderFormHandler)
	mux.HandleFunc("POST /order", submitOrderHandler)
	mux.HandleFunc("GET /api/menu", apiMenuHandler)
	mux.HandleFunc("GET /admin", adminHandler)
	mux.HandleFunc("POST /admin/login", adminLoginHandler)
	mux.HandleFunc("POST /admin/logout", adminLogoutHandler)
	mux.HandleFunc("POST /admin/orders/{id}/deliver", markDeliveredHandler)
	mux.HandleFunc("POST /admin/orders/{id}/undeliver", markUndeliveredHandler)
	mux.HandleFunc("POST /admin/orders/{id}/delete", deleteOrderHandler)
	mux.HandleFunc("POST /admin/menu", saveMenuHandler)
	mux.HandleFunc("POST /admin/menu/{date}/hide", hideMenuHandler)
	mux.HandleFunc("POST /admin/menu/{date}/show", showMenuHandler)
	mux.HandleFunc("POST /admin/menu/{date}/delete", deleteMenuHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("Сервер запущен на http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
