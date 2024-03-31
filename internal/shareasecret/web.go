package shareasecret

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// mapRoutes maps all HTTP routes for the application.
func (a *Application) mapRoutes() {
	fs := http.FileServer(http.Dir("./static/"))
	a.router.Handle("GET /static/", http.StripPrefix("/static/", fs))
	a.router.Handle("GET /robots.txt", serveFile("./static/robots.txt"))

	a.router.HandleFunc("GET /", a.handleGetIndex)

	a.router.Handle("GET /nojs", templ.Handler(pageNoJavascript()))
	a.router.Handle("GET /oops", templ.Handler(pageOops()))

	a.router.HandleFunc("POST /secret", a.handleCreateSecret)
	a.router.HandleFunc("GET /secret/{viewingID}", a.handleGetSecret)
	a.router.HandleFunc("GET /manage-secret/{managementID}", a.handleManageSecret)
	a.router.HandleFunc("POST /manage-secret/{managementID}/delete", a.handleDeleteSecret)
}

func (a *Application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	loggingHandler(
		a.router,
	).ServeHTTP(w, r)
}

func loggingHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := log.With().
			Str("url", r.URL.String()).
			Str("method", r.Method)

		r = r.WithContext(l.Logger().WithContext(r.Context()))

		h.ServeHTTP(w, r)
	})
}

func serveFile(fileName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fileName)
	})
}

func (a *Application) handleGetIndex(w http.ResponseWriter, r *http.Request) {
	pageIndex(notificationsFromRequest(r, w)).Render(r.Context(), w)
}

func (a *Application) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	l := zerolog.Ctx(r.Context())
	secret := ""
	ttl := 0

	// parse and validate the request
	if err := r.ParseForm(); err != nil {
		badRequest("Unable to parse request form. Please try again.", w)
		return
	} else {
		// very little we can do here aside from validating the structure of the "encrypted" text string received matches
		// how the front-end should have formatted it
		secret = r.Form.Get("encryptedSecret")
		if strings.Count(secret, ".") != 2 {
			badRequest("Secret format is invalid. Please try again.", w)
			return
		}

		ttl, err = strconv.Atoi(r.Form.Get("ttl"))
		if err != nil {
			badRequest("Unable to parse the TTL (time to live) for the secret.", w)
			return
		}
	}

	// create the secret, and generate two cryptographically random, 192 bit identifiers to use for viewing and
	// management of the secret respectively
	viewingID, err := secureID()
	if err != nil {
		l.Err(err).Msg("generating viewing id")
		internalServerError(w)
		return
	}

	managementID, err := secureID()
	if err != nil {
		l.Err(err).Msg("generating management id")
		internalServerError(w)
		return
	}

	if _, err := a.db.db.Exec(
		`
			INSERT INTO
				secrets (viewing_id, management_id, cipher_text, ttl, created_at)
			VALUES
				(?, ?, ?, ?, ?)
		`,
		viewingID,
		managementID,
		secret,
		ttl,
		time.Now().UnixMilli(),
	); err != nil {
		l.Err(err).Msg("creating secret")
		internalServerError(w)
		return
	}

	// redirect the user to the manage secrets page
	http.Redirect(w, r, fmt.Sprintf("/manage-secret/%s", managementID), http.StatusCreated)
}

func (a *Application) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	l := zerolog.Ctx(r.Context())
	viewingID := r.PathValue("viewingID")

	// retrieve the cipher text for the relevant secret, or return an error if that secret cannot be found
	var cipherText string

	err := a.db.db.QueryRow(
		`
			SELECT
				cipher_text
			FROM
				secrets
			WHERE
				viewing_id = ? AND
				deleted_at IS NULL
		`,
		viewingID,
	).Scan(&cipherText)

	if errors.Is(sql.ErrNoRows, err) {
		setFlashErr("Secret does not exist or has been deleted.", w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if err != nil {
		l.Err(err).Str("viewing_id", viewingID).Msg("retrieving secret")
		http.Redirect(w, r, "/oops", http.StatusSeeOther)
		return
	}

	pageViewSecret(cipherText, notificationsFromRequest(r, w)).Render(r.Context(), w)
}

func (a *Application) handleManageSecret(w http.ResponseWriter, r *http.Request) {
	l := zerolog.Ctx(r.Context())
	managementID := r.PathValue("managementID")

	// retrieve the ID in order to view and decrypt the secret, or return an error if that secret cannot be found
	var secretID string

	err := a.db.db.QueryRow(
		`
			SELECT
				viewing_id
			FROM
				secrets
			WHERE
				management_id = ? AND
				deleted_at IS NULL
		`,
		managementID,
	).Scan(&secretID)

	if errors.Is(sql.ErrNoRows, err) {
		setFlashErr("Secret does not exist or has been deleted.", w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if err != nil {
		l.Err(err).Str("management_id", managementID).Msg("retrieving secret")
		http.Redirect(w, r, "/oops", http.StatusSeeOther)
		return
	}

	pageManageSecret(
		managementID,
		fmt.Sprintf("%s/secret/%s", a.baseURL, secretID),
		fmt.Sprintf("%s/manage-secret/%s/delete", a.baseURL, managementID),
		notificationsFromRequest(r, w),
	).Render(r.Context(), w)
}

func (a *Application) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	l := zerolog.Ctx(r.Context())
	managementID := r.PathValue("managementID")

	// delete the secret, returning the user to the manage secret page with an error message if that fails
	_, err := a.db.db.Exec(
		"UPDATE secrets SET deleted_at = ?, deletion_reason = ?, cipher_text = NULL WHERE management_id = ?",
		time.Now().UnixMilli(),
		deletionReasonUserDeleted,
		managementID,
	)
	if err != nil {
		l.Err(err).Str("management_id", managementID).Msg("deleting secret")
		http.Redirect(w, r, "/oops", http.StatusSeeOther)
		return
	}

	setFlashSuccess("Secret successfully deleted.", w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func badRequest(err string, w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte(err))
}

func internalServerError(w http.ResponseWriter) {
	w.WriteHeader(http.StatusInternalServerError)
}

func setFlashErr(msg string, w http.ResponseWriter) {
	setFlash("err", msg, w)
}

func setFlashSuccess(msg string, w http.ResponseWriter) {
	setFlash("success", msg, w)
}

func setFlash(name string, msg string, w http.ResponseWriter) {
	n := fmt.Sprintf("flash_%s", name)
	http.SetCookie(w, &http.Cookie{Name: n, Value: msg, Path: "/"})
}

func notificationsFromRequest(r *http.Request, w http.ResponseWriter) notifications {
	return notifications{
		errorMsg:   flash("err", r, w),
		successMsg: flash("success", r, w),
	}
}

func flash(name string, r *http.Request, w http.ResponseWriter) string {
	n := fmt.Sprintf("flash_%s", name)

	// read the cookie, returning an empty string if it doesn't exist
	c, err := r.Cookie(n)
	if err != nil {
		return ""
	}

	// set a cookie with the same name so it is "expired" within the client's browser
	http.SetCookie(
		w,
		&http.Cookie{
			Name:    n,
			Value:   "",
			Expires: time.Unix(1, 0),
			MaxAge:  -1,
			Path:    "/",
		},
	)

	return c.Value
}

func secureID() (string, error) {
	b := make([]byte, 24)

	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}
