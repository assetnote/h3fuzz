package webserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGeneratedWebPasswordPersistsWithDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	first, err := New(Config{DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	firstPassword := first.BasicAuthPassword()
	if err := validatePersistentWebPassword(firstPassword); err != nil {
		t.Fatalf("generated password = %q: %v", firstPassword, err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Config{DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.store.Close() })
	if second.BasicAuthPassword() != firstPassword {
		t.Fatalf("password changed across restart: first=%q second=%q", firstPassword, second.BasicAuthPassword())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	request.SetBasicAuth("h3fuzz", firstPassword)
	recorder := httptest.NewRecorder()
	second.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("persisted credentials returned status %d: %s", recorder.Code, recorder.Body.String())
	}

	settingsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	settingsRequest.SetBasicAuth("h3fuzz", firstPassword)
	settingsRecorder := httptest.NewRecorder()
	second.Handler().ServeHTTP(settingsRecorder, settingsRequest)
	if strings.Contains(settingsRecorder.Body.String(), firstPassword) {
		t.Fatal("persisted authentication password was exposed by the settings API")
	}

	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		info, statErr := os.Stat(path)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
			t.Fatalf("credential database file %s permissions = %04o, want no group or world access", path, permissions)
		}
	}
}

func TestPersistentWebPasswordCreationIsConcurrentSafe(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const callers = 32
	passwords := make(chan string, callers)
	errors := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			password, passwordErr := store.persistentWebPassword(context.Background())
			if passwordErr != nil {
				errors <- passwordErr
				return
			}
			passwords <- password
		}()
	}
	wait.Wait()
	close(passwords)
	close(errors)
	for err := range errors {
		t.Errorf("persistentWebPassword: %v", err)
	}

	var expected string
	for password := range passwords {
		if expected == "" {
			expected = password
		}
		if password != expected {
			t.Errorf("concurrent caller received %q, want %q", password, expected)
		}
	}
	if err := validatePersistentWebPassword(expected); err != nil {
		t.Fatalf("persisted password = %q: %v", expected, err)
	}
}

func TestExplicitWebPasswordDoesNotReplacePersistedPassword(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	generated, err := New(Config{DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	persistedPassword := generated.BasicAuthPassword()
	if err := generated.store.Close(); err != nil {
		t.Fatal(err)
	}

	explicit, err := New(Config{DatabasePath: databasePath, Password: "operator-supplied"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.BasicAuthPassword() != "operator-supplied" {
		t.Fatalf("explicit password = %q", explicit.BasicAuthPassword())
	}
	if err := explicit.store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Config{DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.store.Close() })
	if restarted.BasicAuthPassword() != persistedPassword {
		t.Fatalf("explicit override replaced persisted password: got %q, want %q", restarted.BasicAuthPassword(), persistedPassword)
	}
}

func TestCorruptPersistentWebPasswordFailsClosed(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO web_auth (id, password, created_at) VALUES (1, 'not-a-generated-password', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{DatabasePath: databasePath})
	if server != nil {
		_ = server.store.Close()
		t.Fatal("New returned a server with a corrupt persisted password")
	}
	if err == nil || !strings.Contains(err.Error(), "persisted web password is corrupt") {
		t.Fatalf("New error = %v, want corrupt password error", err)
	}
}
