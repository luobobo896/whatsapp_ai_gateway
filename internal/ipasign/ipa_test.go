package ipasign

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractEmbeddedProvision(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "wda.ipa")
	body := []byte(personalPlist)
	if err := writeFakeIPA(ipa, body); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractEmbeddedProvision(ipa)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("extracted %d bytes, want %d", len(got), len(body))
	}
	p, err := ParseProvisionBytes(got)
	if err != nil {
		t.Fatal(err)
	}
	if p.DetectedMode() != ModePersonal || p.UUID == "" || p.TeamName == "" {
		t.Fatalf("%+v", p)
	}
}

func TestExtractEmbeddedProvisionMissing(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "empty.ipa")
	if err := writeZip(ipa, "Payload/README.txt", []byte("no")); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractEmbeddedProvision(ipa); err == nil {
		t.Fatal("expected missing provision")
	}
}

func writeFakeIPA(path string, provision []byte) error {
	return writeZip(path, "Payload/WebDriverAgentRunner-Runner.app/embedded.mobileprovision", provision)
}

func writeZip(path, name string, body []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(name)
	if err != nil {
		_ = zw.Close()
		return err
	}
	if _, err := w.Write(body); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}
