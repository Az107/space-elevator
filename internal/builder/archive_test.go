package builder

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := "/srv/drops/abc"
	cases := []struct {
		name    string
		wantErr bool
		want    string
	}{
		{"index.html", false, "/srv/drops/abc/index.html"},
		{"./a/b.txt", false, "/srv/drops/abc/a/b.txt"},
		{"/etc/passwd", false, "/srv/drops/abc/etc/passwd"}, // leading slash stripped
		{"../../etc/passwd", true, ""},
		{"a/../../escape", true, ""},
		{"..", true, ""},
		{"", false, root},
	}
	for _, tc := range cases {
		got, err := safeJoin(root, tc.name)
		if tc.wantErr && err == nil {
			t.Errorf("safeJoin(%q) = %q, want error", tc.name, got)
		}
		if !tc.wantErr {
			if err != nil {
				t.Errorf("safeJoin(%q): unexpected error %v", tc.name, err)
			} else if got != tc.want {
				t.Errorf("safeJoin(%q) = %q, want %q", tc.name, got, tc.want)
			}
		}
	}
}

func buildTarGz(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	path := filepath.Join(t.TempDir(), "drop.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractAndDetectStatic(t *testing.T) {
	src := buildTarGz(t, map[string]string{"index.html": "<h1>hi</h1>", "css/app.css": "body{}"})
	dest := t.TempDir()
	if err := ExtractArchive(src, dest); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectKind(dest)
	if err != nil {
		t.Fatal(err)
	}
	if kind != DropKindStatic {
		t.Errorf("kind = %q, want static", kind)
	}
	b, err := os.ReadFile(filepath.Join(dest, "index.html"))
	if err != nil || string(b) != "<h1>hi</h1>" {
		t.Errorf("extracted file wrong: %q err=%v", b, err)
	}
}

func TestDetectKindDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := DetectKind(dir)
	if err != nil {
		t.Fatal(err)
	}
	if kind != DropKindDockerfile {
		t.Errorf("kind = %q, want dockerfile", kind)
	}
	if !HasDockerfile(dir) {
		t.Error("HasDockerfile = false, want true")
	}
}

func TestExtractEntryCap(t *testing.T) {
	c := &extractCounters{}
	for i := 0; i < extractMaxEntries; i++ {
		if err := c.addEntry(0); err != nil {
			t.Fatalf("entry %d should be allowed", i+1)
		}
	}
	if err := c.addEntry(0); err == nil {
		t.Error("entry cap should trigger")
	}
}

func TestExtractSizeCap(t *testing.T) {
	c := &extractCounters{}
	if err := c.addEntry(extractMaxTotalBytes); err != nil {
		t.Fatal(err)
	}
	if err := c.addEntry(1); err == nil {
		t.Error("total size cap should trigger")
	}
}

// A zip whose central directory claims a tiny size but whose stream
// decompresses huge must never write a huge file: extraction either
// fails (Go's zip reader rejects the size lie) or writes ≤ the per-file
// cap via the LimitReader. Both are acceptable; disk-filling is not.
func TestZipBombTruncation(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("bomb.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("A"), 2<<20)); err != nil { // 2 MiB
		t.Fatal(err)
	}
	zw.Close()
	path := filepath.Join(t.TempDir(), "bomb.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	// Patch the central directory size so it lies (as crafted bombs do).
	data, _ := os.ReadFile(path)
	if i := bytes.Index(data, []byte("PK\x01\x02")); i >= 0 {
		// Uncompressed size field sits at offset 24 within the central header.
		data[i+24] = 0x10
		data[i+25] = 0x00
		data[i+26] = 0x00
		data[i+27] = 0x00
		_ = os.WriteFile(path, data, 0o644)
	}
	err = extractZip(path, dest, &extractCounters{})
	if err == nil {
		info, statErr := os.Stat(filepath.Join(dest, "bomb.bin"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Size() > extractMaxFileBytes {
			t.Fatalf("lying-size zip wrote %d bytes; cap is %d", info.Size(), extractMaxFileBytes)
		}
	}
	// An error is equally fine — as long as nothing huge landed on disk.
	entries, _ := os.ReadDir(dest)
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	if total > extractMaxFileBytes {
		t.Fatalf("extraction wrote %d bytes from a bomb zip", total)
	}
}

func TestZipSlipRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("nope"))
	zw.Close()
	path := filepath.Join(t.TempDir(), "slip.zip")
	os.WriteFile(path, buf.Bytes(), 0o644)
	dest := t.TempDir()
	if err := extractZip(path, dest, &extractCounters{}); err == nil {
		t.Fatal("zip-slip must be rejected")
	}
	if _, err := os.Stat(filepath.Join(dest, "evil.txt")); err == nil {
		t.Error("nothing should be extracted outside dest")
	}
}
