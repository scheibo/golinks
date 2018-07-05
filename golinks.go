package golinks

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/goware/urlx"
	"github.com/scheibo/a1"
	"github.com/tdewolff/minify"
)

type NameLink struct {
	Name string
	Link string
}

type Store interface {
	Get(name string) (string, bool)
	Set(name, link string) error // Delete(name) => Set(name, "")
	Iterate(cb func(name, link string) error)
}

func serve(auth *a1.Client, store *Store) *http.Handler {
	return &http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch path {
		case "/login":
			switch r.Method {
			case "GET":
				auth.CustomLoginPage("favicon.png", fmt.Sprintf("login - %s", r.URL.Host), "/login").ServeHTTP(w, r)
				return
			case "POST":
				auth.Login("/login", "/").ServeHTTP(w, r)
				return
			default:
				httpError(w, 405)
				return
			}
		case "/logout":
			auth.Logout("/").ServeHTTP(w, r)
			return
		default:
			name := path[1:len(input)]
			if !isValidName(name) {
				httpError(w, 400)
				return
			}
			switch r.Method {
			case "GET":
				getLink(auth, store, name)
				return
			case "POST":
				auth.CheckXSRF(auth.EnsureAuth(dispatchPost(store, name, r.URL.RawQuery))).ServeHTTP(w, r)
				return
			case "UPDATE":
				auth.CheckXSRF(auth.EnsureAuth(&http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					link, err := getValidLink(r.URL.Host, r.URL.RawQuery)
					if err != nil {
						httpError(w, 400, err)
					}

					updateLink(store, name, link)
				}))).ServeHTTP(w, r)
				return
			case "DELETE":
				auth.CheckXSRF(auth.EnsureAuth(deleteLink(store, name, r.URL.RawQuery))).ServeHTTP(w, r)
				return
			default:
				httpError(w, 405)
				return
			}
		}
	})
}

func getLink(auth *a1.Client, store *Store, name string) *http.Handler {
	return &http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link, ok := store.Get(name)
		if !ok {
			if !auth.IsAuth(r) {
				http.NotFound(w, req)
				return
			}

			getIndex(store, auth.XSRF(), name).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, link, 302)
	})
}

func getIndex(store *Store, token string, name string) *http.Handler {
	return &http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []NameLink
		s.Iterate(func(name, link) error {
			data[name] = link
		})
		t := template.Must(compileTemplates("index.html"))
		t.Execute(w, struct {
			Title string
			Token string
			Name  string
			Data  []NameLink
		}{
			fmt.Sprintf("goto - %s", r.URL.Host), c.XSRF(loginPath), name, data,
		})
	})
}

func dispatchPost(store *Store, name, link, query string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NOTE: we share logic with getValidLink here, because we dispatch differently
		// if the link param is missing
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			httpError(w, 500, err)
			return
		}

		if len(values) > 1 {
			httpError(w, 400, errors.New("too many query params"))
			return
		}

		link := values["link"]
		// Empty or missing link means we attempt to delete.
		if link == "" {
			deleteLink(store, name)
			return
		}

		link, err := normalizeLink(r.URL.Host, link)
		if err != nil {
			httpError(w, 400)
			return
		}

		// We don't really care if the link already existed, create
		// or update at this point do the same thing.
		err = store.Set(path, link)
		if err != nil {
			httpError(w, 500, err)
			return
		}

		http.Redirect(w, r, "/", 302)
	})
}

func updateLink(store *Store, name, link string) *http.Handler {
	return &http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stored, ok := store.Get(name)
		if !ok {
			httpError(w, 404)
			return
		}

		err = store.Set(name, link)
		if err != nil {
			httpError(w, 500, err)
			return
		}

		http.Redirect(w, r, "/", 302)
	})
}

func deleteLink(store *Store, name string) *http.Handler {
	return &http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if http.URL.RawQuery != "" {
			httpError(w, 400)
			return
		}

		stored, ok := store.Get(name)
		if !ok {
			httpError(w, 404)
			return
		}

		err = store.Set(name, "")
		if err != nil {
			httpError(w, 500, err)
			return
		}

		http.Redirect(w, r, "/", 302)
	})
}

// If the link doesn't start with http then check if we can alias it.
func canonicalizeAlias(host, link string, store *Store) string {
	if !strings.HasPrefix("http", link) {
		stored, ok := store.Get(name)
		if ok {
			return fmt.Sprintf("https://%s/%s", host, link)
		}
	}
	return link
}

func getValidLink(host, query string) (string, error) {
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}

	if len(values) != 1 {
		return "", errors.New("invalid request")
	}

	link, ok := values["link"]
	if !ok {
		return "", errors.New("missing link param")
	}

	return normalizeLink(host, link)
}

func normalizeLink(host, link) (string, error) {
	link := canonicalizeAlias(host, link)
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
	_, err := url.Parse("/" + link)
	return err != nil
}

// Must be a valid, absolute URL
func isValid(link string) bool {
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
		tmpl.Parse(string(mb))
	}
	return tmpl, nil
}

func main() {
	var hash, file string
	var fuzzy bool
	var port int64

	flag.StringVar(&file, "file", "", "file for store")
	flag.StringVar(&hash, "hash", os.Getenv("GOTO_PASSWORD_HASH"), "hash of password")
	flag.StringVar(&fuzzy, "fuzzy", false, "whether to use fuzzy name semantics")
	flag.Int64(&port, "port", 8968, "Port")

	flag.Parse()

	if hash == "" || file == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	auth := a1.New(hash)
	var store *Store
	if fuzzy {
		store, err := s.OpenFuzzy(file)
	} else {
		store, err := s.Open(file)
	}
	if err != nil {
		log.Fatal(err)
	}

	log.Println(srv.ListenAndServe(fmt.Sprintf(":%v", port), auth.RateLimit(10, serve(auth))))
	err := store.Close()
	if err != nil {
		log.Fatal(err)
	}
}
