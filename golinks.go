package golinks

import (
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goware/urlx"
	"github.com/scheibo/a1"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/html"
)

type NameLink struct {
	Name string
	Link string
}

type Store interface {
	Get(name string) (string, bool)
	Set(name, link string) error // Delete(name) => Set(name, "")
	Iterate(cb func(name, link string) error) error
}

func serve(auth *a1.Client, store Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch path {
		case "/login":
			switch r.Method {
			case "GET":
				auth.CustomLoginPage("favicon.png", fmt.Sprintf("login - %s", r.URL.Host), "/login").ServeHTTP(w, r)
			case "POST":
				auth.Login("/login", "/").ServeHTTP(w, r)
			default:
				httpError(w, 405)
			}
		case "/logout":
			auth.Logout("/").ServeHTTP(w, r)
		default:
			name := path[1:]
			if !isValidName(name) {
				httpError(w, 400)
				return
			}
			switch r.Method {
			case "GET":
				getLink(auth, store, name)
			case "POST", "UPDATE":
				update := r.Method == "UPDATE"
				auth.CheckXSRF(auth.EnsureAuth(postLink(store, name, update))).ServeHTTP(w, r)
			case "DELETE":
				auth.CheckXSRF(auth.EnsureAuth(deleteLink(store, name))).ServeHTTP(w, r)
			default:
				httpError(w, 405)
			}
		}
	})
}

func getLink(auth *a1.Client, store Store, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link, ok := store.Get(name)
		if !ok {
			if !auth.IsAuth(r) {
				http.NotFound(w, r)
				return
			}

			getIndex(store, auth.XSRF(), name).ServeHTTP(w, r)
		}
		http.Redirect(w, r, link, 302)
	})
}

func getIndex(store Store, token string, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []NameLink
		err := store.Iterate(func(name, link string) error {
			data = append(data, NameLink{Name: name, Link: link})
			return nil
		})
		if err != nil {
			httpError(w, 500, err)
			return
		}
		t := template.Must(compileTemplates("index.html"))
		_ = t.Execute(w, struct {
			Title string
			Token string
			Name  string
			Data  []NameLink
		}{
			fmt.Sprintf("goto - %s", r.URL.Host), token, name, data,
		})
	})
}

func postLink(store Store, name string, update bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			httpError(w, 500, err)
			return
		}

		if len(values) > 1 {
			httpError(w, 400, errors.New("too many query params"))
			return
		}

		links, ok := values["link"]
		if !ok {
			httpError(w, 400, errors.New("missing link param"))
		}

		if len(links) > 1 {
			httpError(w, 400, errors.New("too many link params"))
			return
		}

		link := links[0]
		// Empty or missing link means we attempt to delete.
		if link == "" {
			deleteLink(store, name)
			return
		}

		link, err = normalizeLink(canonicalizeAlias(store, r.URL.Host, link))
		if err != nil {
			httpError(w, 400)
			return
		}

		// UPDATE should onlyu work on links which already existed
		if update {
			_, ok := store.Get(name)
			if !ok {
				httpError(w, 404)
				return
			}
		}

		err = store.Set(name, link)
		if err != nil {
			httpError(w, 500, err)
			return
		}

		http.Redirect(w, r, "/", 302)
	})
}

func deleteLink(store Store, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			httpError(w, 400)
			return
		}

		_, ok := store.Get(name)
		if !ok {
			httpError(w, 404)
			return
		}

		err := store.Set(name, "")
		if err != nil {
			httpError(w, 500, err)
			return
		}

		http.Redirect(w, r, "/", 302)
	})
}

// If the link doesn't start with http then check if we can alias it.
func canonicalizeAlias(store Store, host, link string) string {
	if !strings.HasPrefix("http", link) {
		link = strings.TrimPrefix(link, "go/")
		_, ok := store.Get(link)
		if ok {
			return fmt.Sprintf("https://%s/%s", host, link)
		}
	}
	return link
}

func normalizeLink(link string) (string, error) {
	err := errors.New("invalid link")
	if !isValidLink(link) {
		return "", err
	}

	// Normalize
	u, err := urlx.Parse(link)
	if err != nil {
		return "", err
	}
	normal, err := urlx.Normalize(u)
	if err != nil {
		return "", err
	}

	return normal, nil
}

// Must be valid path, url.Parse handles paths parsing for us.
func isValidName(name string) bool {
	if name == "login" || name == "logout" {
		// shouldn't be possible anyway, but reject just in case
		return false
	}
	_, err := url.Parse("/" + name)
	return err != nil
}

// Must be a valid, absolute URL
func isValidLink(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return u.IsAbs()
}

func httpError(w http.ResponseWriter, code int, err ...error) {
	msg := http.StatusText(code)
	if len(err) > 0 {
		msg = fmt.Sprintf("%s: %s", msg, err[0].Error())
	}
	http.Error(w, msg, code)
}

func compileTemplates(filenames ...string) (*template.Template, error) {
	m := minify.New()
	m.AddFunc("text/html", html.Minify)

	var tmpl *template.Template
	for _, filename := range filenames {
		name := filepath.Base(filename)
		if tmpl == nil {
			tmpl = template.New(name)
		} else {
			tmpl = tmpl.New(name)
		}

		b, err := ioutil.ReadFile(filename)
		if err != nil {
			return nil, err
		}

		mb, err := m.Bytes("text/html", b)
		if err != nil {
			return nil, err
		}
		_, err = tmpl.Parse(string(mb))
		if err != nil {
			return nil, err
		}
	}
	return tmpl, nil
}

func main() {
	var hash, file string
	var fuzzy bool
	var port int64

	flag.StringVar(&file, "file", "", "file for store")
	flag.StringVar(&hash, "hash", os.Getenv("GOTO_PASSWORD_HASH"), "hash of password")
	flag.BoolVar(&fuzzy, "fuzzy", false, "whether to use fuzzy name semantics")
	flag.Int64Var(&port, "port", 8968, "Port")

	flag.Parse()

	if hash == "" || file == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	auth := a1.New(hash)
	store, err := Open(file, fuzzy)
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Addr:         fmt.Sprintf(":%v", port),
		Handler:      a1.RateLimit(serve(auth, store), 10),
	}

	log.Println(srv.ListenAndServe())
	err = store.Close()
	if err != nil {
		log.Fatal(err)
	}
}
