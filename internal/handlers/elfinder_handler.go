package handlers

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/services"
)

type ElfinderHandler struct {
	base           *BaseHandler
	websiteService *services.WebsiteService
}

func NewElfinderHandler(cfg *config.Config, sessions *middleware.SessionManager, ws *services.WebsiteService) *ElfinderHandler {
	return &ElfinderHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, websiteService: ws}
}

const volID = "l1_"

func elfHash(rel string) string {
	if rel == "" || rel == "." {
		rel = "/"
	} else if rel[0] != '/' {
		rel = "/" + rel
	}
	return volID + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(rel))
}

func elfUnhash(hash string) (string, error) {
	if !strings.HasPrefix(hash, volID) {
		return "", fmt.Errorf("invalid hash")
	}
	b, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(hash[len(volID):])
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func elfSafe(root, rel string) (string, error) {
	if rel == "" || rel == "/" {
		return root, nil
	}
	joined := filepath.Join(root, filepath.Clean(rel))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	if !strings.HasPrefix(abs, root) {
		return "", fmt.Errorf("path escapes root")
	}
	return abs, nil
}

type elfEntry struct {
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	PHash  string `json:"phash,omitempty"`
	Mime   string `json:"mime"`
	Ts     int64  `json:"ts"`
	Size   int64  `json:"size"`
	Dirs   int    `json:"dirs,omitempty"`
	Read   int    `json:"read"`
	Write  int    `json:"write"`
	Locked int    `json:"locked"`
	VolID  string `json:"volumeid,omitempty"`
}

func buildElfEntry(root, abs string, info fs.FileInfo) elfEntry {
	rel, _ := filepath.Rel(root, abs)
	if rel == "." {
		rel = ""
	}
	parentRel, _ := filepath.Rel(root, filepath.Dir(abs))
	if parentRel == "." {
		parentRel = ""
	}

	e := elfEntry{
		Name:   info.Name(),
		Hash:   elfHash(rel),
		Ts:     info.ModTime().Unix(),
		Size:   info.Size(),
		Read:   1,
		Write:  1,
		Locked: 0,
	}

	if rel == "" {
		e.Name = "Root"
		e.VolID = volID
	} else {
		e.PHash = elfHash(parentRel)
	}

	if info.IsDir() {
		e.Mime = "directory"
		e.Size = 0
		if hasSub, _ := hasSubDirs(abs); hasSub {
			e.Dirs = 1
		}
	} else {
		ext := filepath.Ext(info.Name())
		mt := mime.TypeByExtension(ext)
		if mt == "" {
			mt = "application/octet-stream"
		}
		e.Mime = mt
	}

	return e
}

func hasSubDirs(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, d := range entries {
		if d.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func listElfDir(root, dir string) []elfEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []elfEntry
	for _, d := range entries {
		info, err := d.Info()
		if err != nil {
			continue
		}
		result = append(result, buildElfEntry(root, filepath.Join(dir, d.Name()), info))
	}
	sort.Slice(result, func(i, j int) bool {
		if (result[i].Mime == "directory") != (result[j].Mime == "directory") {
			return result[i].Mime == "directory"
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func (h *ElfinderHandler) siteRoot(c *fiber.Ctx) (string, error) {
	var id uint
	if _, err := fmt.Sscanf(c.Params("id"), "%d", &id); err != nil || id == 0 {
		return "", fmt.Errorf("invalid id")
	}
	site, err := h.websiteService.Find(id)
	if err != nil {
		return "", fmt.Errorf("website not found")
	}
	root := site.RootPath
	if root == "" {
		root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + site.Name
	}
	abs, _ := filepath.Abs(root)
	return abs, nil
}

func (h *ElfinderHandler) resolveTarget(root, hash string) (string, error) {
	if hash == "" {
		return root, nil
	}
	rel, err := elfUnhash(hash)
	if err != nil {
		return root, nil
	}
	return elfSafe(root, rel)
}

func (h *ElfinderHandler) rootEntry(root string) elfEntry {
	info, err := os.Stat(root)
	if err != nil {
		return elfEntry{Name: "Root", Hash: elfHash(""), Mime: "directory", Read: 1, Write: 1, VolID: volID}
	}
	return buildElfEntry(root, root, info)
}

// Connector dispatches elFinder commands.
func (h *ElfinderHandler) Connector(c *fiber.Ctx) error {
	root, err := h.siteRoot(c)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	_ = os.MkdirAll(root, 0o755)

	cmd := c.FormValue("cmd", c.Query("cmd"))
	switch cmd {
	case "open":
		return h.cmdOpen(c, root)
	case "tree":
		return h.cmdTree(c, root)
	case "parents":
		return h.cmdParents(c, root)
	case "ls":
		return h.cmdLs(c, root)
	case "mkdir":
		return h.cmdMkdir(c, root)
	case "mkfile":
		return h.cmdMkfile(c, root)
	case "rename":
		return h.cmdRename(c, root)
	case "rm":
		return h.cmdRm(c, root)
	case "upload":
		return h.cmdUpload(c, root)
	case "get":
		return h.cmdGet(c, root)
	case "put":
		return h.cmdPut(c, root)
	case "paste":
		return h.cmdPaste(c, root)
	case "search":
		return h.cmdSearch(c, root)
	case "size":
		return h.cmdSize(c, root)
	case "file":
		return h.cmdFile(c, root)
	default:
		return c.JSON(fiber.Map{"error": "unknown command: " + cmd})
	}
}

func (h *ElfinderHandler) cmdOpen(c *fiber.Ctx, root string) error {
	init := c.FormValue("init", c.Query("init"))
	target := c.FormValue("target", c.Query("target"))

	dir := root
	if init != "1" && target != "" {
		d, err := h.resolveTarget(root, target)
		if err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		dir = d
	}

	info, err := os.Stat(dir)
	if err != nil {
		_ = os.MkdirAll(dir, 0o755)
		info, err = os.Stat(dir)
		if err != nil {
			return c.JSON(fiber.Map{"error": "cannot open directory"})
		}
	}

	cwd := buildElfEntry(root, dir, info)
	files := listElfDir(root, dir)

	var allFiles []elfEntry
	if init == "1" {
		rootE := h.rootEntry(root)
		allFiles = append(allFiles, rootE)
	}
	allFiles = append(allFiles, files...)

	resp := fiber.Map{
		"cwd":     cwd,
		"files":   allFiles,
		"options": elfOptions(root, dir, c.Params("id")),
	}
	if init == "1" {
		resp["api"] = "2.1"
		resp["uplMaxSize"] = "128M"
		resp["netDrivers"] = []string{}
	}
	return c.JSON(resp)
}

func elfOptions(root, dir, siteID string) fiber.Map {
	rel, _ := filepath.Rel(root, dir)
	if rel == "." {
		rel = "/"
	} else {
		rel = "/" + rel
	}
	return fiber.Map{
		"path":      rel,
		"separator": string(filepath.Separator),
		"url":       "/websites/" + siteID + "/elfinder?cmd=file&target=",
		"tmbUrl":    "",
		"disabled":  []string{},
		"archivers": fiber.Map{"create": []string{}, "extract": []string{}},
	}
}

func (h *ElfinderHandler) cmdTree(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	dir, _ := h.resolveTarget(root, target)
	if dir == "" {
		dir = root
	}

	var tree []elfEntry
	entries, _ := os.ReadDir(dir)
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		tree = append(tree, buildElfEntry(root, filepath.Join(dir, d.Name()), info))
	}
	if tree == nil {
		tree = []elfEntry{}
	}
	return c.JSON(fiber.Map{"tree": tree})
}

func (h *ElfinderHandler) cmdParents(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	dir, _ := h.resolveTarget(root, target)
	if dir == "" {
		dir = root
	}

	var tree []elfEntry
	current := dir
	for current != root && strings.HasPrefix(current, root) {
		parent := filepath.Dir(current)
		entries, _ := os.ReadDir(parent)
		for _, d := range entries {
			if !d.IsDir() {
				continue
			}
			info, err := d.Info()
			if err != nil {
				continue
			}
			tree = append(tree, buildElfEntry(root, filepath.Join(parent, d.Name()), info))
		}
		current = parent
	}
	if tree == nil {
		tree = []elfEntry{}
	}
	return c.JSON(fiber.Map{"tree": tree})
}

func (h *ElfinderHandler) cmdLs(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	dir, _ := h.resolveTarget(root, target)
	if dir == "" {
		dir = root
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	return c.JSON(fiber.Map{"list": names})
}

func (h *ElfinderHandler) cmdMkdir(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	name := c.FormValue("name", c.Query("name"))
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	newDir := filepath.Join(dir, filepath.Base(name))
	if !strings.HasPrefix(newDir, root) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(newDir)
	entry := buildElfEntry(root, newDir, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdMkfile(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	name := c.FormValue("name", c.Query("name"))
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	fp := filepath.Join(dir, filepath.Base(name))
	if !strings.HasPrefix(fp, root) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}
	f, err := os.Create(fp)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	_ = f.Close()
	info, _ := os.Stat(fp)
	entry := buildElfEntry(root, fp, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdRename(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	name := c.FormValue("name", c.Query("name"))
	abs, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	if abs == root {
		return c.JSON(fiber.Map{"error": "cannot rename root"})
	}
	newPath := filepath.Join(filepath.Dir(abs), filepath.Base(name))
	if !strings.HasPrefix(newPath, root) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}

	oldInfo, _ := os.Stat(abs)
	oldEntry := buildElfEntry(root, abs, oldInfo)

	if err := os.Rename(abs, newPath); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(newPath)
	entry := buildElfEntry(root, newPath, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}, "removed": []string{oldEntry.Hash}})
}

func (h *ElfinderHandler) cmdRm(c *fiber.Ctx, root string) error {
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}
	var removed []string
	for _, t := range targets {
		hash := string(t)
		abs, err := h.resolveTarget(root, hash)
		if err != nil || abs == root {
			continue
		}
		_ = os.RemoveAll(abs)
		removed = append(removed, hash)
	}
	return c.JSON(fiber.Map{"removed": removed})
}

func (h *ElfinderHandler) cmdUpload(c *fiber.Ctx, root string) error {
	target := c.FormValue("target")
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	_ = os.MkdirAll(dir, 0o755)

	form, err := c.MultipartForm()
	if err != nil {
		return c.JSON(fiber.Map{"error": "no files"})
	}

	var added []elfEntry
	for _, fh := range form.File["upload[]"] {
		name := filepath.Base(fh.Filename)
		if name == "." || name == ".." {
			continue
		}
		dest := filepath.Join(dir, name)
		if !strings.HasPrefix(dest, root) {
			continue
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			_ = src.Close()
			continue
		}
		_, _ = io.Copy(out, src)
		_ = out.Close()
		_ = src.Close()
		info, _ := os.Stat(dest)
		if info != nil {
			added = append(added, buildElfEntry(root, dest, info))
		}
	}
	return c.JSON(fiber.Map{"added": added})
}

func (h *ElfinderHandler) cmdGet(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	abs, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"content": string(data)})
}

func (h *ElfinderHandler) cmdPut(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	content := c.FormValue("content", c.Query("content"))
	abs, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(abs)
	entry := buildElfEntry(root, abs, info)
	return c.JSON(fiber.Map{"changed": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdPaste(c *fiber.Ctx, root string) error {
	dst := c.FormValue("dst")
	cut := c.FormValue("cut") == "1"
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}

	destDir, err := h.resolveTarget(root, dst)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	_ = os.MkdirAll(destDir, 0o755)

	var added []elfEntry
	var removed []string
	for _, t := range targets {
		hash := string(t)
		srcPath, err := h.resolveTarget(root, hash)
		if err != nil || srcPath == root {
			continue
		}
		name := filepath.Base(srcPath)
		dstPath := filepath.Join(destDir, name)
		if !strings.HasPrefix(dstPath, root) {
			continue
		}
		if cut {
			_ = os.Rename(srcPath, dstPath)
			removed = append(removed, hash)
		} else {
			_ = elfCopyPath(srcPath, dstPath)
		}
		info, err := os.Stat(dstPath)
		if err == nil {
			added = append(added, buildElfEntry(root, dstPath, info))
		}
	}
	return c.JSON(fiber.Map{"added": added, "removed": removed})
}

func (h *ElfinderHandler) cmdSearch(c *fiber.Ctx, root string) error {
	q := strings.ToLower(c.FormValue("q", c.Query("q")))
	target := c.FormValue("target", c.Query("target"))
	dir, _ := h.resolveTarget(root, target)
	if dir == "" {
		dir = root
	}

	var results []elfEntry
	const max = 200
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(results) >= max {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), q) {
			info, err := d.Info()
			if err == nil {
				results = append(results, buildElfEntry(root, path, info))
			}
		}
		return nil
	})
	if results == nil {
		results = []elfEntry{}
	}
	return c.JSON(fiber.Map{"files": results})
}

func (h *ElfinderHandler) cmdSize(c *fiber.Ctx, root string) error {
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}
	var total int64
	for _, t := range targets {
		abs, err := h.resolveTarget(root, string(t))
		if err != nil {
			continue
		}
		_ = filepath.WalkDir(abs, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err == nil && !info.IsDir() {
				total += info.Size()
			}
			return nil
		})
	}
	return c.JSON(fiber.Map{"size": total})
}

func (h *ElfinderHandler) cmdFile(c *fiber.Ctx, root string) error {
	target := c.FormValue("target", c.Query("target"))
	abs, err := h.resolveTarget(root, target)
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return c.Status(404).SendString("not found")
	}
	dl := c.Query("download") == "1"
	if dl {
		return c.Download(abs, filepath.Base(abs))
	}
	return c.SendFile(abs)
}

func elfCopyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return elfCopyDir(src, dst)
	}
	return elfCopyFile(src, dst)
}

func elfCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func elfCopyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := elfCopyDir(s, d); err != nil {
				return err
			}
		} else {
			if err := elfCopyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}
