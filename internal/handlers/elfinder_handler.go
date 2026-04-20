package handlers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/services"
	"deploycp/internal/system"
)

type ElfinderHandler struct {
	base           *BaseHandler
	websiteService *services.WebsiteService
	platform       platform.Adapter
	runner         *system.Runner
}

func NewElfinderHandler(cfg *config.Config, sessions *middleware.SessionManager, ws *services.WebsiteService, platformAdapter platform.Adapter, runner *system.Runner) *ElfinderHandler {
	return &ElfinderHandler{
		base:           &BaseHandler{Config: cfg, Sessions: sessions},
		websiteService: ws,
		platform:       platformAdapter,
		runner:         runner,
	}
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
	if !pathWithinRoot(root, abs) {
		return "", fmt.Errorf("path escapes root")
	}
	return abs, nil
}

func pathWithinRoot(root, candidate string) bool {
	cleanRoot := filepath.Clean(root)
	cleanCandidate := filepath.Clean(candidate)
	rel, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	Owner  string `json:"owner,omitempty"`
	Group  string `json:"group,omitempty"`
	Perms  string `json:"perms,omitempty"`
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
		Perms:  fmt.Sprintf("%04o", info.Mode().Perm()),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if u, err := user.LookupId(strconv.Itoa(int(stat.Uid))); err == nil {
			e.Owner = u.Username
		} else {
			e.Owner = strconv.Itoa(int(stat.Uid))
		}
		if g, err := user.LookupGroupId(strconv.Itoa(int(stat.Gid))); err == nil {
			e.Group = g.Name
		} else {
			e.Group = strconv.Itoa(int(stat.Gid))
		}
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

func (h *ElfinderHandler) siteContext(c *fiber.Ctx) (*models.Website, string, error) {
	var id uint
	if _, err := fmt.Sscanf(c.Params("id"), "%d", &id); err != nil || id == 0 {
		return nil, "", fmt.Errorf("invalid id")
	}
	site, err := h.websiteService.Find(id)
	if err != nil {
		return nil, "", fmt.Errorf("website not found")
	}
	root := site.RootPath
	if root == "" {
		root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + site.Name
	}
	// File manager root is the platform home (parent of htdocs), not the web root.
	platformHome := root
	clean := filepath.Clean(root)
	if filepath.Base(clean) == "htdocs" {
		platformHome = filepath.Dir(clean)
	}
	abs, _ := filepath.Abs(platformHome)
	return site, abs, nil
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

// Connector dispatches file manager commands.
func (h *ElfinderHandler) Connector(c *fiber.Ctx) error {
	site, root, err := h.siteContext(c)
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
		return h.cmdMkdir(c, site, root)
	case "mkfile":
		return h.cmdMkfile(c, site, root)
	case "rename":
		return h.cmdRename(c, site, root)
	case "rm":
		return h.cmdRm(c, site, root)
	case "upload":
		return h.cmdUpload(c, site, root)
	case "get":
		return h.cmdGet(c, root)
	case "put":
		return h.cmdPut(c, site, root)
	case "paste":
		return h.cmdPaste(c, site, root)
	case "search":
		return h.cmdSearch(c, root)
	case "size":
		return h.cmdSize(c, root)
	case "file":
		return h.cmdFile(c, root)
	case "chmod":
		return h.cmdChmod(c, site, root)
	case "archive":
		return h.cmdArchive(c, site, root)
	case "extract":
		return h.cmdExtract(c, site, root)
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
		"archivers": fiber.Map{"create": []string{"application/zip"}, "extract": []string{"application/zip", "application/x-gzip", "application/x-tar"}},
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
	for current != root && pathWithinRoot(root, current) {
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

func (h *ElfinderHandler) cmdMkdir(c *fiber.Ctx, site *models.Website, root string) error {
	target := c.FormValue("target", c.Query("target"))
	name := c.FormValue("name", c.Query("name"))
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	newDir := filepath.Join(dir, filepath.Base(name))
	if !pathWithinRoot(root, newDir) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}
	if err := h.mkdirsForSite(c.Context(), site, newDir, 0o775); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(newDir)
	entry := buildElfEntry(root, newDir, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdMkfile(c *fiber.Ctx, site *models.Website, root string) error {
	target := c.FormValue("target", c.Query("target"))
	name := c.FormValue("name", c.Query("name"))
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	fp := filepath.Join(dir, filepath.Base(name))
	if !pathWithinRoot(root, fp) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}
	if err := h.writeFileForSite(c.Context(), site, fp, []byte{}, 0o664); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(fp)
	entry := buildElfEntry(root, fp, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdRename(c *fiber.Ctx, site *models.Website, root string) error {
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
	if !pathWithinRoot(root, newPath) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}

	oldInfo, _ := os.Stat(abs)
	oldEntry := buildElfEntry(root, abs, oldInfo)

	if err := h.renameForSite(c.Context(), site, abs, newPath); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(newPath)
	entry := buildElfEntry(root, newPath, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}, "removed": []string{oldEntry.Hash}})
}

func (h *ElfinderHandler) cmdRm(c *fiber.Ctx, site *models.Website, root string) error {
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
		_ = h.removePathForSite(c.Context(), site, abs)
		removed = append(removed, hash)
	}
	return c.JSON(fiber.Map{"removed": removed})
}

func (h *ElfinderHandler) cmdUpload(c *fiber.Ctx, site *models.Website, root string) error {
	target := c.FormValue("target")
	dir, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	_ = os.MkdirAll(dir, 0o775)

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
		if !pathWithinRoot(root, dest) {
			continue
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		content, readErr := io.ReadAll(src)
		_ = src.Close()
		if readErr != nil {
			continue
		}
		if err := h.writeFileForSite(c.Context(), site, dest, content, 0o664); err != nil {
			continue
		}
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

func (h *ElfinderHandler) cmdPut(c *fiber.Ctx, site *models.Website, root string) error {
	target := c.FormValue("target", c.Query("target"))
	content := c.FormValue("content", c.Query("content"))
	abs, err := h.resolveTarget(root, target)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	if err := h.writeFileForSite(c.Context(), site, abs, []byte(content), 0o664); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	info, _ := os.Stat(abs)
	entry := buildElfEntry(root, abs, info)
	return c.JSON(fiber.Map{"changed": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdPaste(c *fiber.Ctx, site *models.Website, root string) error {
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
		if !pathWithinRoot(root, dstPath) {
			continue
		}
		dstPath, err = nextAvailablePath(dstPath)
		if err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		if cut {
			if err := h.renameForSite(c.Context(), site, srcPath, dstPath); err != nil {
				return c.JSON(fiber.Map{"error": err.Error()})
			}
			removed = append(removed, hash)
		} else {
			if err := h.copyPathForSite(c.Context(), site, srcPath, dstPath); err != nil {
				return c.JSON(fiber.Map{"error": err.Error()})
			}
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

func nextAvailablePath(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, nil
	} else if err != nil {
		return "", err
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if ext == ".gz" && strings.HasSuffix(strings.ToLower(base), ".tar.gz") {
		ext = ".tar.gz"
		name = strings.TrimSuffix(base, ext)
	}

	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", name, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("unable to determine available name")
}

func (h *ElfinderHandler) cmdChmod(c *fiber.Ctx, site *models.Website, root string) error {
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}
	if len(targets) == 0 {
		target := c.FormValue("target", c.Query("target"))
		if target != "" {
			targets = [][]byte{[]byte(target)}
		}
	}
	mode := c.FormValue("mode", c.Query("mode"))
	if mode == "" {
		return c.JSON(fiber.Map{"error": "mode is required (e.g. 0755)"})
	}
	perm, err := strconv.ParseUint(strings.TrimPrefix(mode, "0"), 8, 32)
	if err != nil || perm > 0o7777 {
		return c.JSON(fiber.Map{"error": "invalid permission mode"})
	}
	var changed []elfEntry
	for _, t := range targets {
		abs, err := h.resolveTarget(root, string(t))
		if err != nil {
			continue
		}
		if err := h.chmodForSite(c.Context(), site, abs, os.FileMode(perm)); err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		info, _ := os.Stat(abs)
		if info != nil {
			changed = append(changed, buildElfEntry(root, abs, info))
		}
	}
	return c.JSON(fiber.Map{"changed": changed})
}

func (h *ElfinderHandler) cmdArchive(c *fiber.Ctx, site *models.Website, root string) error {
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}
	if len(targets) == 0 {
		return c.JSON(fiber.Map{"error": "no targets"})
	}
	name := strings.TrimSpace(c.FormValue("name", c.Query("name")))
	if name == "" {
		name = "archive.zip"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	firstAbs, err := h.resolveTarget(root, string(targets[0]))
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	parentDir := filepath.Dir(firstAbs)
	archivePath := filepath.Join(parentDir, filepath.Base(name))
	if !pathWithinRoot(root, archivePath) {
		return c.JSON(fiber.Map{"error": "path escapes root"})
	}
	// Collect file names relative to parentDir.
	var names []string
	for _, t := range targets {
		abs, err := h.resolveTarget(root, string(t))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(parentDir, abs)
		if err != nil {
			continue
		}
		names = append(names, rel)
	}
	if len(names) == 0 {
		return c.JSON(fiber.Map{"error": "no valid targets"})
	}
	if err := writeZipArchive(archivePath, parentDir, names); err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	_ = h.ensureOwnedBySiteUser(c.Context(), site, archivePath)
	info, err := os.Stat(archivePath)
	if err != nil {
		return c.JSON(fiber.Map{"error": err.Error()})
	}
	entry := buildElfEntry(root, archivePath, info)
	return c.JSON(fiber.Map{"added": []elfEntry{entry}})
}

func (h *ElfinderHandler) cmdExtract(c *fiber.Ctx, site *models.Website, root string) error {
	targets := c.Request().PostArgs().PeekMulti("targets[]")
	if len(targets) == 0 {
		if v := c.FormValue("targets[]"); v != "" {
			targets = [][]byte{[]byte(v)}
		}
	}
	if len(targets) == 0 {
		target := c.FormValue("target", c.Query("target"))
		if target != "" {
			targets = [][]byte{[]byte(target)}
		}
	}
	if len(targets) == 0 {
		return c.JSON(fiber.Map{"error": "no targets"})
	}

	var added []elfEntry
	for _, t := range targets {
		abs, err := h.resolveTarget(root, string(t))
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		destDir, err := uniqueExtractDir(filepath.Dir(abs), info.Name())
		if err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		if !pathWithinRoot(root, destDir) {
			return c.JSON(fiber.Map{"error": "path escapes root"})
		}
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		switch {
		case strings.HasSuffix(strings.ToLower(info.Name()), ".zip"):
			err = extractZipArchive(abs, destDir)
		case strings.HasSuffix(strings.ToLower(info.Name()), ".tar.gz"), strings.HasSuffix(strings.ToLower(info.Name()), ".tgz"):
			err = extractTarGzArchive(abs, destDir)
		case strings.HasSuffix(strings.ToLower(info.Name()), ".tar"):
			err = extractTarArchive(abs, destDir)
		default:
			err = fmt.Errorf("unsupported archive format")
		}
		if err != nil {
			return c.JSON(fiber.Map{"error": err.Error()})
		}
		extractedInfo, statErr := os.Stat(destDir)
		if statErr == nil {
			_ = h.ensureOwnedBySiteUser(c.Context(), site, destDir)
			added = append(added, buildElfEntry(root, destDir, extractedInfo))
		}
	}
	if len(added) == 0 {
		return c.JSON(fiber.Map{"error": "no valid archives"})
	}
	return c.JSON(fiber.Map{"added": added})
}

func uniqueExtractDir(parentDir, archiveName string) (string, error) {
	base := archiveName
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		base = base[:len(base)-7]
	case strings.HasSuffix(lower, ".tgz"):
		base = base[:len(base)-4]
	case strings.HasSuffix(lower, ".zip"), strings.HasSuffix(lower, ".tar"):
		base = base[:len(base)-4]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "extracted"
	}
	dest := filepath.Join(parentDir, base)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest, nil
	}
	for i := 2; i < 1000; i++ {
		candidate := filepath.Join(parentDir, fmt.Sprintf("%s-%d", base, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unable to choose extract directory")
}

func writeZipArchive(zipPath, parentDir string, names []string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	for _, name := range names {
		srcPath := filepath.Join(parentDir, name)
		if err := addPathToZip(zw, parentDir, srcPath); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}

func addPathToZip(zw *zip.Writer, baseDir, srcPath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(baseDir, srcPath)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if info.IsDir() {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel + "/"
		if _, err := zw.CreateHeader(header); err != nil {
			return err
		}
		entries, err := os.ReadDir(srcPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := addPathToZip(zw, baseDir, filepath.Join(srcPath, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = rel
	header.Method = zip.Deflate
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(writer, in)
	return err
}

func extractZipArchive(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, file := range zr.File {
		target, err := safeArchiveJoin(destDir, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func extractTarGzArchive(tarGzPath, destDir string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()
	return extractTarReader(tar.NewReader(gzr), destDir)
}

func extractTarArchive(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTarReader(tar.NewReader(f), destDir)
}

func extractTarReader(tr *tar.Reader, destDir string) error {
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeArchiveJoin(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func safeArchiveJoin(destDir, entryName string) (string, error) {
	cleanDest := filepath.Clean(destDir)
	cleanEntry := filepath.Clean(strings.TrimLeft(entryName, `/\`))
	target := filepath.Join(cleanDest, cleanEntry)
	prefix := cleanDest + string(filepath.Separator)
	if target != cleanDest && !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("archive escapes destination")
	}
	return target, nil
}

func (h *ElfinderHandler) siteUsername(site *models.Website) string {
	if site == nil || site.SiteUser == nil || strings.TrimSpace(site.SiteUser.Username) == "" {
		return ""
	}
	return strings.TrimSpace(site.SiteUser.Username)
}

func (h *ElfinderHandler) canRunAsSiteUser(site *models.Website) bool {
	if h.siteUsername(site) == "" || h.runner == nil || h.platform == nil {
		return false
	}
	if h.base.Config.Features.PlatformMode == "dryrun" {
		return false
	}
	return h.platform.Name() == "linux" && strings.TrimSpace(h.base.Config.Paths.RunuserBinary) != ""
}

func (h *ElfinderHandler) runAsSiteUser(ctx context.Context, site *models.Website, binary string, args []string, stdin string) error {
	username := h.siteUsername(site)
	if !h.canRunAsSiteUser(site) {
		return fmt.Errorf("site user execution unavailable")
	}
	runArgs := []string{"-u", username, "--", binary}
	runArgs = append(runArgs, args...)
	_, err := h.runner.Run(ctx, system.CommandRequest{
		Binary:      h.base.Config.Paths.RunuserBinary,
		Args:        runArgs,
		Stdin:       stdin,
		Timeout:     60 * time.Second,
		AuditAction: "filemanager.run_as_site_user",
		ActorUserID: nil,
	})
	return err
}

func (h *ElfinderHandler) ensureOwnedBySiteUser(ctx context.Context, site *models.Website, path string) error {
	username := h.siteUsername(site)
	if username == "" || h.platform == nil {
		return nil
	}
	return h.platform.Users().ChownRecursive(ctx, username, path)
}

func (h *ElfinderHandler) mkdirsForSite(ctx context.Context, site *models.Website, dir string, perm os.FileMode) error {
	if h.canRunAsSiteUser(site) {
		if err := h.runAsSiteUser(ctx, site, "/bin/mkdir", []string{"-p", dir}, ""); err != nil {
			return err
		}
		return h.chmodForSite(ctx, site, dir, perm)
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	return h.ensureOwnedBySiteUser(ctx, site, dir)
}

func (h *ElfinderHandler) chmodForSite(ctx context.Context, site *models.Website, path string, perm os.FileMode) error {
	mode := fmt.Sprintf("%04o", perm.Perm())
	if h.canRunAsSiteUser(site) {
		if err := h.runAsSiteUser(ctx, site, "/bin/chmod", []string{mode, path}, ""); err == nil {
			return nil
		}
	}
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	return h.ensureOwnedBySiteUser(ctx, site, path)
}

func (h *ElfinderHandler) writeFileForSite(ctx context.Context, site *models.Website, path string, content []byte, perm os.FileMode) error {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	if h.canRunAsSiteUser(site) {
		if err := h.runAsSiteUser(ctx, site, "/bin/dd", []string{"of=" + path, "status=none"}, string(content)); err != nil {
			return err
		}
		if existed {
			return nil
		}
		return h.chmodForSite(ctx, site, path, perm)
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return err
	}
	return h.ensureOwnedBySiteUser(ctx, site, path)
}

func (h *ElfinderHandler) renameForSite(ctx context.Context, site *models.Website, oldPath, newPath string) error {
	if h.canRunAsSiteUser(site) {
		return h.runAsSiteUser(ctx, site, "/bin/mv", []string{oldPath, newPath}, "")
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	return h.ensureOwnedBySiteUser(ctx, site, newPath)
}

func (h *ElfinderHandler) removePathForSite(ctx context.Context, site *models.Website, path string) error {
	if h.canRunAsSiteUser(site) {
		return h.runAsSiteUser(ctx, site, "/bin/rm", []string{"-rf", path}, "")
	}
	return os.RemoveAll(path)
}

func (h *ElfinderHandler) copyPathForSite(ctx context.Context, site *models.Website, srcPath, dstPath string) error {
	if h.canRunAsSiteUser(site) {
		return h.runAsSiteUser(ctx, site, "/bin/cp", []string{"-a", srcPath, dstPath}, "")
	}
	if err := elfCopyPath(srcPath, dstPath); err != nil {
		return err
	}
	return h.ensureOwnedBySiteUser(ctx, site, dstPath)
}
