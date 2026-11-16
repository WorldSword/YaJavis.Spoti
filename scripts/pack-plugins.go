//go:build ignore

// Упаковать официальные плагины из plugins/<id>/ в <out>/plugin-<id>.zip для релиза.
//
//	go run scripts/pack-plugins.go build/release
//
// Проверяет, что каждый плагин есть в plugins/index.json с той же версией.
package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/pack-plugins.go <папка>")
		os.Exit(2)
	}
	out := os.Args[1]
	must(os.MkdirAll(out, 0o755))
	var idx struct {
		Plugins []struct{ ID, Version string } `json:"plugins"`
	}
	b, err := os.ReadFile("plugins/index.json")
	must(err)
	must(json.Unmarshal(b, &idx))
	want := map[string]string{}
	for _, p := range idx.Plugins {
		want[p.ID] = p.Version
	}
	ents, err := os.ReadDir("plugins")
	must(err)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("plugins", e.Name())
		var m struct{ ID, Version, Kind, Main string }
		b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			continue
		}
		must(json.Unmarshal(b, &m))
		if m.ID != e.Name() {
			fail("%s: id в plugin.json (%s) не совпадает с папкой", dir, m.ID)
		}
		if v, ok := want[m.ID]; !ok || v != m.Version {
			fail("%s: в plugins/index.json версия %q, в plugin.json %q", m.ID, v, m.Version)
		}
		if m.Kind != "js" {
			if m.Kind != "native" {
				fail("%s: неизвестный kind", m.ID)
			}
			for _, arch := range []string{"arm64", "armv7"} {
				stage, err := os.MkdirTemp("", "yajavis-plugin-")
				must(err)
				must(os.WriteFile(filepath.Join(stage, "plugin.json"), b, 0644))
				bin := strings.ReplaceAll(m.Main, "{arch}", arch)
				if filepath.Base(bin) != bin {
					fail("неправильный main: %s", bin)
				}
				goBin := os.Getenv("GO")
				if goBin == "" {
					goBin = "go"
				}
				target, _ := filepath.Abs(filepath.Join(stage, bin))
				cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w -buildid=", "-o", target, ".")
				cmd.Dir = filepath.Join(dir, "src")
				goarch := arch
				if arch == "armv7" {
					goarch = "arm"
				}
				for _, v := range os.Environ() {
					if !strings.HasPrefix(v, "GOOS=") && !strings.HasPrefix(v, "GOARCH=") && !strings.HasPrefix(v, "GOARM=") && !strings.HasPrefix(v, "CGO_ENABLED=") {
						cmd.Env = append(cmd.Env, v)
					}
				}
				cmd.Env = append(cmd.Env, "GOOS=linux", "GOARCH="+goarch, "GOARM=7", "CGO_ENABLED=0")
				if output, err := cmd.CombinedOutput(); err != nil {
					fail("%s %s: %v\n%s", m.ID, arch, err, output)
				}
				pack(stage, filepath.Join(out, "plugin-"+m.ID+"-"+arch+".zip"))
				must(os.RemoveAll(stage))
				fmt.Printf("plugin-%s-%s.zip %s\n", m.ID, arch, m.Version)
			}
			continue
		}
		dst := filepath.Join(out, "plugin-"+m.ID+".zip")
		pack(dir, dst)
		info, _ := os.Stat(dst)
		fmt.Printf("%s %s — %d КБ\n", filepath.Base(dst), m.Version, (info.Size()+1023)/1024)
	}
}

func pack(dir, dst string) {
	var names []string
	must(filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	}))
	sort.Strings(names)
	f, err := os.Create(dst)
	must(err)
	zw := zip.NewWriter(f)
	for _, p := range names {
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		must(err)
		h := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()}
		info, err := os.Stat(p)
		must(err)
		h.SetMode(info.Mode())
		w, err := zw.CreateHeader(h)
		must(err)
		if filepath.Ext(p) == ".json" || filepath.Ext(p) == ".js" {
			b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
		}
		_, err = w.Write(b)
		must(err)
	}
	must(zw.Close())
	must(f.Close())
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
