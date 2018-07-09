package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goware/urlx"
	"github.com/scheibo/a1"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	"github.com/tdewolff/minify/html"
	"github.com/tdewolff/minify/js"
	"github.com/tdewolff/minify/svg"
)

// NameLink holds a (name, link) pair for rendering.
type NameLink struct {
	Name string
	Link string
}

// Store provides the ability to get/set and iterate through name -> link pairs,
type Store interface {
	// Get returns the link and true Set for name, or "" and false if it doesn't exist.
	Get(name string) (string, bool)
	// Set associates a link with a name. Set can be used to 'delete' a mapping by
	// specifying "" as the link.
	Set(name, link string) error
	// Iterates through all the (name, link) pairs stored in the order they were last Set.
	// If cb returns an error the iteration is stopped and Iterate will return with the same error.
	Iterate(cb func(name, link string) error) error
}

var healthy int32

// serve acts as the router for the application: "favicon.ico", "/login", "/logout" are
// treated specially, everything else will either add or display mappings from name to links.
func serve(auth *a1.Client, store Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		log.Printf("%s %s\n", r.Method, path)
		switch path {
		case "/healthz":
			healthz().ServeHTTP(w, r)
		case "/favicon.ico":
			http.ServeFile(w, r, "favicon.ico")
		case "/login":
			switch r.Method {
			case "GET":
				auth.CustomLoginPage("favicon.ico", fmt.Sprintf("login - %s", r.Host), "/login").ServeHTTP(w, r)
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
				// NOTE: we only check auth within getLink as sometimes we redirect.
				getLink(auth, store, name).ServeHTTP(w, r)
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

// getLink is the handler for any GET request - if we know of a mapping we redirect, otherwise
// we check auth and render the index with the name already filled into the new entry field.
func getLink(auth *a1.Client, store Store, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link, ok := store.Get(name)
		if !ok {
			if !auth.IsAuth(r) {
				http.Redirect(w, r, "/login", 302)
				return
			}

			getIndex(store, auth.XSRF(), name).ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, link, 302)
	})
}

// getIndex renders the index of all saved name -> link mappings for an authed user.
func getIndex(store Store, token string, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []NameLink
		_ = store.Iterate(func(name, link string) error {
			data = append(data, NameLink{Name: name, Link: link})
			return nil
		})

		t := template.Must(compileTemplates("index.html"))
		_ = t.Execute(w, struct {
			Title string
			Token string
			Name  string
			Data  []NameLink
		}{
			fmt.Sprintf("goto - %s", r.Host), token, name, data,
		})
	})
}

// postLink handlers creating new mappings or updating/deleting mappings from name to
// the link parameter it receives in the request. If update is true, this will only support
// updating already existing mappings.
func postLink(store Store, name string, update bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.PostFormValue("name")
		link := r.PostFormValue("link")

		// Empty or missing link means we attempt to delete.
		if link == "" {
			if n != name {
				httpError(w, 400)
				return
			}
			deleteLink(store, name).ServeHTTP(w, r)
			return
		}

		// If link we actually an alias ("name" or "go/name") instead of a URL, we convert it.
		// We also normalize the link so everything follows a uniform pattern.
		link, err := normalizeLink(canonicalizeAlias(store, r.Host, link))
		if err != nil {
			httpError(w, 400)
			return
		}

		// If the name in the form body is present and doesn't match name then we delete the
		// original name and use the name from the body instead/
		del := ""
		if n != "" && n != name {
			del = name
			name = n
		}

		// UPDATE should only work on links which already existed
		if update {
			_, ok := store.Get(name)
			if !ok {
				httpError(w, 404)
				return
			}
		}

		if del != "" {
			err = store.Set(del, "")
			if err != nil {
				httpError(w, 500, err)
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

// deleteLink removes any mappings for name from the store.
func deleteLink(store Store, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// canonicalizeAliases turns a link 'alias' into the correct absolute URL. Aliases
// are of the form "name" or "go/name" provided "name" exists in the store.
// We canonicalize the alias to point to the full link with the specified host.
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

// normalizeLink ensures link is valid and then normalizes it so all links follow the
// same uniform pattern.
func normalizeLink(link string) (string, error) {
	err := errors.New("invalid link")
	if !isValidLink(link) {
		return "", err
	}

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

// isValidName confirms that name is a valid path.
func isValidName(name string) bool {
	if name == "healthz" ||
		name == "favicon.ico" ||
		name == "login" ||
		name == "logout" {
		// shouldn't be possible anyway, but reject just in case
		return false
	}

	// this also should be somewhat redundant - if the name wasn't valid how
	// did we get here in the first place?
	_, err := url.Parse("/" + name)
	return err == nil
}

// isValidLink confirms that link is a valid, absolute URL.
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
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("text/html", html.Minify)
	m.AddFunc("text/javascript", js.Minify)
	m.AddFunc("image/svg+xml", svg.Minify)

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

func healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&healthy) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
}

func start(srv *http.Server) {
	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		atomic.StoreInt32(&healthy, 0)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("Could not gracefully shutdown the srv: %v\n", err)
		}
		close(done)
	}()

	atomic.StoreInt32(&healthy, 1)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Could not listen on %s: %v\n", srv.Addr, err)
	}

	<-done
}

func main() {
	var hash, file, dump string
	var fuzzy bool
	var port int64

	flag.StringVar(&file, "file", "", "file for store")
	flag.StringVar(&dump, "dump", "", "optional file to dump cleaned store to")
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
	if dump != "" {
		err = store.Dump(dump)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Set up the server with timeouts such that it can be used in production. Furthermore, we rate
	// limit our actions to 10 QPS for some slight mitigation against scanning attacks. Note: this
	// will not prevent a motivated attacker - URLs which are secret or do not have their own auth
	// should not be used with *any* URL shortening service.
	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Addr:         fmt.Sprintf(":%v", port),
		Handler:      a1.RateLimit(10, serve(auth, store)),
	}

	start(srv)

	err = store.Close()
	if err != nil {
		log.Fatal(err)
	}
}
