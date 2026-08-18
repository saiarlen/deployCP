package restrictedtransfer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

const (
	SFTPCommand       = "restricted-sftp"
	SCPCommand        = "restricted-scp"
	ShellCommand      = "restricted-shell"
	SandboxConfigPath = "/etc/deploycp/restricted-shell.json"
	ShellRCPath       = "/usr/local/libexec/deploycp-shell.rc"
)

// Run handles the privileged transfer-only commands before the main
// application is initialized. The caller must be the managed sudo rule.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 || (args[0] != SFTPCommand && args[0] != SCPCommand && args[0] != ShellCommand) {
		return false, nil
	}
	account, uid, gid, err := lookupSudoAccount()
	if err != nil {
		return true, err
	}
	if args[0] == ShellCommand {
		return true, serveRestrictedShell(account, uid, gid, stdin, stdout, stderr)
	}
	if err := enterUserChroot(account, uid, gid); err != nil {
		return true, err
	}
	switch args[0] {
	case SFTPCommand:
		return true, serveSFTP(account.HomeDir, stdin, stdout)
	case SCPCommand:
		if len(args) < 2 {
			return true, errors.New("missing SCP server command")
		}
		return true, serveSCP(strings.Join(args[1:], " "), account.HomeDir, stdin, stdout)
	default:
		return false, nil
	}
}

func lookupSudoAccount() (*user.User, int, int, error) {
	if os.Geteuid() != 0 {
		return nil, 0, 0, errors.New("restricted access helper must run through sudo")
	}
	username := strings.TrimSpace(os.Getenv("SUDO_USER"))
	if username == "" || username == "root" {
		return nil, 0, 0, errors.New("restricted access user is unavailable")
	}
	account, err := user.Lookup(username)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("lookup restricted access user: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 {
		return nil, 0, 0, errors.New("invalid restricted access uid")
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid <= 0 {
		return nil, 0, 0, errors.New("invalid restricted access gid")
	}
	return account, uid, gid, nil
}

func enterUserChroot(account *user.User, uid, gid int) error {
	root, err := filepath.EvalSymlinks(filepath.Clean(account.HomeDir))
	if err != nil {
		return fmt.Errorf("resolve restricted transfer root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.New("restricted transfer root is not a directory")
	}

	groupIDs, err := account.GroupIds()
	if err != nil {
		return fmt.Errorf("resolve restricted transfer groups: %w", err)
	}
	groups := make([]int, 0, len(groupIDs))
	for _, raw := range groupIDs {
		value, convErr := strconv.Atoi(raw)
		if convErr == nil && value >= 0 {
			groups = append(groups, value)
		}
	}
	if err := syscall.Chroot(root); err != nil {
		return fmt.Errorf("chroot restricted transfer: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("enter restricted transfer root: %w", err)
	}
	if err := syscall.Setgroups(groups); err != nil {
		return fmt.Errorf("drop restricted transfer groups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("drop restricted transfer gid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("drop restricted transfer uid: %w", err)
	}
	if os.Geteuid() != uid || os.Getegid() != gid {
		return errors.New("restricted transfer privilege drop failed")
	}
	return nil
}

type sandboxConfig struct {
	RuntimeRoot string `json:"runtime_root"`
}

func serveRestrictedShell(account *user.User, uid, gid int, stdin io.Reader, stdout, stderr io.Writer) error {
	if account == nil {
		return errors.New("restricted shell account is unavailable")
	}
	home, err := filepath.EvalSymlinks(filepath.Clean(account.HomeDir))
	if err != nil {
		return fmt.Errorf("resolve restricted shell home: %w", err)
	}
	if info, statErr := os.Stat(home); statErr != nil || !info.IsDir() {
		return errors.New("restricted shell home is not a directory")
	}
	rawConfig, err := os.ReadFile(SandboxConfigPath)
	if err != nil {
		return fmt.Errorf("read restricted shell configuration: %w", err)
	}
	var cfg sandboxConfig
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return fmt.Errorf("parse restricted shell configuration: %w", err)
	}
	runtimeRoot := filepath.Clean(strings.TrimSpace(cfg.RuntimeRoot))
	if runtimeRoot == "." || !filepath.IsAbs(runtimeRoot) {
		return errors.New("restricted shell runtime root is invalid")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bubblewrap is required for interactive platform SSH")
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		return errors.New("setpriv is required for interactive platform SSH")
	}
	groupIDs, err := supplementaryGroupIDs(account)
	if err != nil {
		return fmt.Errorf("resolve restricted shell groups: %w", err)
	}
	args := buildBubblewrapArgs(account, uid, gid, groupIDs, home, runtimeRoot)
	command := exec.Command(bwrap, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	if err := command.Run(); err != nil {
		return fmt.Errorf("restricted shell sandbox: %w", err)
	}
	return nil
}

func supplementaryGroupIDs(account *user.User) ([]string, error) {
	if account == nil {
		return nil, errors.New("restricted shell account is unavailable")
	}
	rawIDs, err := account.GroupIds()
	if err != nil {
		return nil, err
	}
	return normalizedGroupIDs(rawIDs), nil
}

func normalizedGroupIDs(rawIDs []string) []string {
	seen := make(map[string]struct{}, len(rawIDs))
	groupIDs := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil || value < 0 {
			continue
		}
		id := strconv.Itoa(value)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		groupIDs = append(groupIDs, id)
	}
	return groupIDs
}

func buildBubblewrapArgs(account *user.User, uid, gid int, groupIDs []string, home, runtimeRoot string) []string {
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term == "" || strings.ContainsAny(term, " \t\r\n/") {
		term = "xterm-256color"
	}
	args := []string{
		"--die-with-parent",
		"--unshare-ipc",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-cgroup-try",
		"--hostname", "deploycp-sandbox",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		// Create shared namespace parents explicitly before mounting private
		// files or directories below them. Bubblewrap derives automatically
		// created parent permissions from the mounted source; a 0640 passwd
		// file or 2770 platform home can therefore produce a root-only 0750
		// parent that the site account cannot traverse after setpriv runs.
		"--dir", "/etc",
		"--dir", "/run",
		"--dir", "/var",
		"--symlink", "../run", "/var/run",
		"--clearenv",
		"--setenv", "HOME", home,
		"--setenv", "USER", account.Username,
		"--setenv", "LOGNAME", account.Username,
		"--setenv", "SHELL", "/bin/bash",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "TERM", term,
		"--setenv", "LANG", "C.UTF-8",
	}
	for _, source := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(source); err == nil {
			args = append(args, "--ro-bind", source, source)
		}
	}
	for _, source := range []string{
		"/etc/passwd", "/etc/group", "/etc/shadow", "/etc/gshadow", "/etc/login.defs", "/etc/bash.bashrc",
		"/etc/nsswitch.conf", "/etc/resolv.conf", "/etc/hosts",
		"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/etc/localtime", "/etc/ld.so.cache",
		"/etc/gitconfig", "/etc/machine-id", "/etc/sudo.conf", "/etc/sudoers", "/etc/sudoers.d",
		"/etc/pam.d", "/etc/security", "/run/systemd", "/run/dbus",
	} {
		if _, err := os.Stat(source); err == nil {
			args = append(args, "--ro-bind", source, source)
		}
	}
	if info, err := os.Stat(runtimeRoot); err == nil && info.IsDir() {
		args = append(args, "--ro-bind", runtimeRoot, runtimeRoot)
	}
	args = append(args,
		"--dir", filepath.Dir(home),
		"--bind", home, home,
		"--chdir", home,
		// setpriv uses these two capabilities only long enough to assume the
		// site account. Keep them in the bounding set so the setuid sudo binary
		// can later honor DeployCP's exact, per-runtime systemctl allow-list.
		"--cap-drop", "ALL",
		"--cap-add", "CAP_SETUID",
		"--cap-add", "CAP_SETGID",
		"/usr/bin/setpriv",
		fmt.Sprintf("--reuid=%d", uid),
		fmt.Sprintf("--regid=%d", gid),
	)
	if len(groupIDs) == 0 {
		args = append(args, "--clear-groups")
	} else {
		args = append(args, "--groups="+strings.Join(groupIDs, ","))
	}
	args = append(args,
		"--inh-caps=-all",
		"--ambient-caps=-all",
		"/bin/bash", "--noprofile", "--rcfile", ShellRCPath, "-i",
	)
	return args
}

type stdioReadWriteCloser struct {
	io.Reader
	io.Writer
}

func (stdioReadWriteCloser) Close() error { return nil }

func serveSFTP(originalHome string, stdin io.Reader, stdout io.Writer) error {
	fs := &chrootFS{originalHome: originalHome, root: "/"}
	handlers := sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs}
	server := sftp.NewRequestServer(stdioReadWriteCloser{Reader: stdin, Writer: stdout}, handlers)
	defer server.Close()
	if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("serve restricted SFTP: %w", err)
	}
	return nil
}

type chrootFS struct {
	originalHome string
	root         string
}

type fileInfoLister []os.FileInfo

func (items fileInfoLister) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(items)) {
		return 0, io.EOF
	}
	n := copy(dst, items[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

func virtualPath(raw string) string {
	clean := path.Clean("/" + strings.TrimPrefix(raw, "/"))
	return filepath.FromSlash(clean)
}

func (fs *chrootFS) path(raw string) string {
	virtual := chrootSCPPath(raw, fs.originalHome)
	if fs.root == "" || fs.root == "/" {
		return virtual
	}
	return filepath.Join(fs.root, strings.TrimPrefix(virtual, "/"))
}

func (fs *chrootFS) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	return os.Open(fs.path(request.Filepath))
}

func (fs *chrootFS) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	flags := request.Pflags()
	osFlags := 0
	switch {
	case flags.Read && flags.Write:
		osFlags |= os.O_RDWR
	case flags.Write:
		osFlags |= os.O_WRONLY
	default:
		return nil, os.ErrPermission
	}
	if flags.Creat {
		osFlags |= os.O_CREATE
	}
	if flags.Trunc {
		osFlags |= os.O_TRUNC
	}
	if flags.Excl {
		osFlags |= os.O_EXCL
	}
	// WriterAt and O_APPEND are incompatible. SFTP supplies explicit offsets,
	// so append requests safely use the normal writable descriptor.
	return os.OpenFile(fs.path(request.Filepath), osFlags, 0o666)
}

func (fs *chrootFS) Filecmd(request *sftp.Request) error {
	filePath := fs.path(request.Filepath)
	targetPath := fs.path(request.Target)
	switch request.Method {
	case "Setstat":
		attrs := request.Attributes()
		flags := request.AttrFlags()
		if flags.Size {
			if err := os.Truncate(filePath, int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Permissions {
			if err := os.Chmod(filePath, attrs.FileMode()); err != nil {
				return err
			}
		}
		if flags.UidGid {
			if err := os.Chown(filePath, int(attrs.UID), int(attrs.GID)); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			return os.Chtimes(filePath, attrs.AccessTime(), attrs.ModTime())
		}
		return nil
	case "Rename":
		return os.Rename(filePath, targetPath)
	case "Rmdir":
		return os.Remove(filePath)
	case "Mkdir":
		return os.Mkdir(filePath, 0o777)
	case "Remove":
		return os.Remove(filePath)
	case "Link", "Symlink":
		// Links are deliberately disabled: they are unnecessary for SCP and
		// complicate the virtual-root contract presented to SFTP clients.
		return os.ErrPermission
	default:
		return fmt.Errorf("unsupported SFTP command %q", request.Method)
	}
}

func (fs *chrootFS) Filelist(request *sftp.Request) (sftp.ListerAt, error) {
	filePath := fs.path(request.Filepath)
	switch request.Method {
	case "List":
		entries, err := os.ReadDir(filePath)
		if err != nil {
			return nil, err
		}
		items := make(fileInfoLister, 0, len(entries))
		for _, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			items = append(items, info)
		}
		return items, nil
	case "Stat":
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, err
		}
		return fileInfoLister{info}, nil
	case "Readlink":
		return nil, os.ErrPermission
	default:
		return nil, fmt.Errorf("unsupported SFTP list command %q", request.Method)
	}
}

func (fs *chrootFS) Lstat(request *sftp.Request) (sftp.ListerAt, error) {
	info, err := os.Lstat(fs.path(request.Filepath))
	if err != nil {
		return nil, err
	}
	return fileInfoLister{info}, nil
}

func (fs *chrootFS) RealPath(raw string) (string, error) {
	return filepath.ToSlash(chrootSCPPath(raw, fs.originalHome)), nil
}

type scpServerOptions struct {
	mode      byte
	targetDir bool
	preserve  bool
	recursive bool
	path      string
}

func serveSCP(command, originalHome string, stdin io.Reader, stdout io.Writer) error {
	tokens, err := splitShellCommand(command)
	if err != nil {
		return err
	}
	opts, err := parseSCPServerOptions(tokens)
	if err != nil {
		return err
	}
	opts.path = chrootSCPPath(opts.path, originalHome)
	switch opts.mode {
	case 'f':
		return sendSCP(opts, stdin, stdout)
	case 't':
		return receiveSCP(opts, stdin, stdout)
	default:
		return errors.New("invalid SCP server mode")
	}
}

func splitShellCommand(input string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			tokens = append(tokens, current.String())
			current.Reset()
			started = false
		}
	}
	for _, char := range input {
		if escaped {
			current.WriteRune(char)
			escaped = false
			started = true
			continue
		}
		if quote == '\'' {
			if char == '\'' {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			started = true
			continue
		}
		if quote == '"' {
			switch char {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				current.WriteRune(char)
			}
			started = true
			continue
		}
		switch char {
		case '\\':
			escaped = true
			started = true
		case '\'', '"':
			quote = char
			started = true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			current.WriteRune(char)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("invalid quoted SCP command")
	}
	flush()
	return tokens, nil
}

func parseSCPServerOptions(tokens []string) (scpServerOptions, error) {
	opts := scpServerOptions{}
	if len(tokens) < 3 || filepath.Base(tokens[0]) != "scp" {
		return opts, errors.New("invalid SCP server command")
	}
	endOptions := false
	for _, token := range tokens[1:] {
		if token == "--" && !endOptions {
			endOptions = true
			continue
		}
		if !endOptions && strings.HasPrefix(token, "-") && token != "-" {
			for _, flag := range strings.TrimPrefix(token, "-") {
				switch flag {
				case 't', 'f':
					if opts.mode != 0 && opts.mode != byte(flag) {
						return opts, errors.New("conflicting SCP server modes")
					}
					opts.mode = byte(flag)
				case 'd':
					opts.targetDir = true
				case 'p':
					opts.preserve = true
				case 'r':
					opts.recursive = true
				case 'v':
				default:
					return opts, fmt.Errorf("restricted SCP option -%c is not allowed", flag)
				}
			}
			continue
		}
		if opts.path != "" {
			return opts, errors.New("only one SCP path is allowed")
		}
		opts.path = token
	}
	if opts.mode == 0 || opts.path == "" {
		return opts, errors.New("SCP transfer mode and path are required")
	}
	return opts, nil
}

func chrootSCPPath(raw, originalHome string) string {
	if raw == "~" {
		return "/"
	}
	if strings.HasPrefix(raw, "~/") {
		raw = strings.TrimPrefix(raw, "~/")
	}
	cleanHome := path.Clean(filepath.ToSlash(originalHome))
	cleanRaw := path.Clean(filepath.ToSlash(raw))
	if cleanRaw == cleanHome {
		return "/"
	}
	if strings.HasPrefix(cleanRaw, cleanHome+"/") {
		cleanRaw = strings.TrimPrefix(cleanRaw, cleanHome+"/")
	}
	return virtualPath(cleanRaw)
}

func readSCPAck(reader *bufio.Reader) error {
	code, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	message, _ := reader.ReadString('\n')
	if code == 1 || code == 2 {
		return errors.New(strings.TrimSpace(message))
	}
	return fmt.Errorf("invalid SCP acknowledgement %d", code)
}

func writeSCPAck(writer io.Writer) error {
	_, err := writer.Write([]byte{0})
	return err
}

func writeSCPError(writer io.Writer, err error) {
	message := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
	_, _ = fmt.Fprintf(writer, "\x01%s\n", message)
}

func sendSCP(opts scpServerOptions, stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(stdin)
	if err := readSCPAck(reader); err != nil {
		return err
	}
	info, err := os.Stat(opts.path)
	if err != nil {
		writeSCPError(stdout, err)
		return err
	}
	return sendSCPEntry(reader, stdout, opts.path, info, opts)
}

func sendSCPEntry(reader *bufio.Reader, writer io.Writer, filePath string, info os.FileInfo, opts scpServerOptions) error {
	name := info.Name()
	if strings.ContainsAny(name, "\r\n") {
		return errors.New("SCP filenames may not contain newlines")
	}
	if opts.preserve {
		mtime := info.ModTime().Unix()
		if _, err := fmt.Fprintf(writer, "T%d 0 %d 0\n", mtime, mtime); err != nil {
			return err
		}
		if err := readSCPAck(reader); err != nil {
			return err
		}
	}
	if info.IsDir() {
		if !opts.recursive {
			return errors.New("SCP directory transfer requires -r")
		}
		if _, err := fmt.Fprintf(writer, "D%04o 0 %s\n", info.Mode().Perm(), name); err != nil {
			return err
		}
		if err := readSCPAck(reader); err != nil {
			return err
		}
		entries, err := os.ReadDir(filePath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childPath := filepath.Join(filePath, entry.Name())
			childInfo, err := os.Stat(childPath)
			if err != nil {
				return err
			}
			if err := sendSCPEntry(reader, writer, childPath, childInfo, opts); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer, "E\n"); err != nil {
			return err
		}
		return readSCPAck(reader)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := fmt.Fprintf(writer, "C%04o %d %s\n", info.Mode().Perm(), info.Size(), name); err != nil {
		return err
	}
	if err := readSCPAck(reader); err != nil {
		return err
	}
	if _, err := io.CopyN(writer, file, info.Size()); err != nil {
		return err
	}
	if err := writeSCPAck(writer); err != nil {
		return err
	}
	return readSCPAck(reader)
}

type scpTimes struct {
	mtime time.Time
	atime time.Time
	set   bool
}

func receiveSCP(opts scpServerOptions, stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(stdin)
	targetIsDir := opts.targetDir
	if info, err := os.Stat(opts.path); err == nil && info.IsDir() {
		targetIsDir = true
	}
	if opts.targetDir {
		if info, err := os.Stat(opts.path); err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("SCP target is not a directory")
			}
			writeSCPError(stdout, err)
			return err
		}
	}
	if err := writeSCPAck(stdout); err != nil {
		return err
	}
	_, err := receiveSCPEntries(reader, stdout, opts.path, targetIsDir, opts)
	return err
}

func receiveSCPEntries(reader *bufio.Reader, writer io.Writer, target string, targetIsDir bool, opts scpServerOptions) (bool, error) {
	pendingTimes := scpTimes{}
	received := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && received {
				return false, nil
			}
			return false, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "E" {
			if err := writeSCPAck(writer); err != nil {
				return false, err
			}
			return true, nil
		}
		if line == "" {
			continue
		}
		switch line[0] {
		case 1, 2:
			return false, errors.New(strings.TrimSpace(line[1:]))
		case 'T':
			var mtime, mtimeMicros, atime, atimeMicros int64
			if _, err := fmt.Sscanf(line, "T%d %d %d %d", &mtime, &mtimeMicros, &atime, &atimeMicros); err != nil {
				writeSCPError(writer, err)
				return false, err
			}
			pendingTimes = scpTimes{
				mtime: time.Unix(mtime, mtimeMicros*1000),
				atime: time.Unix(atime, atimeMicros*1000),
				set:   true,
			}
			if err := writeSCPAck(writer); err != nil {
				return false, err
			}
			continue
		case 'C', 'D':
		default:
			err := errors.New("invalid SCP protocol command")
			writeSCPError(writer, err)
			return false, err
		}

		parts := strings.SplitN(line[1:], " ", 3)
		if len(parts) != 3 {
			err := errors.New("invalid SCP file command")
			writeSCPError(writer, err)
			return false, err
		}
		modeValue, err := strconv.ParseUint(parts[0], 8, 32)
		if err != nil {
			writeSCPError(writer, err)
			return false, err
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size < 0 {
			if err == nil {
				err = errors.New("invalid SCP file size")
			}
			writeSCPError(writer, err)
			return false, err
		}
		name := parts[2]
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\r\n") {
			err := errors.New("invalid SCP filename")
			writeSCPError(writer, err)
			return false, err
		}
		destination := target
		if targetIsDir || received {
			destination = filepath.Join(target, name)
		}
		mode := os.FileMode(modeValue) & os.ModePerm
		if line[0] == 'D' {
			if !opts.recursive {
				err := errors.New("SCP directory transfer requires -r")
				writeSCPError(writer, err)
				return false, err
			}
			if err := os.MkdirAll(destination, mode); err != nil {
				writeSCPError(writer, err)
				return false, err
			}
			if err := writeSCPAck(writer); err != nil {
				return false, err
			}
			if _, err := receiveSCPEntries(reader, writer, destination, true, opts); err != nil {
				return false, err
			}
			_ = os.Chmod(destination, mode)
			if pendingTimes.set {
				_ = os.Chtimes(destination, pendingTimes.atime, pendingTimes.mtime)
			}
		} else {
			file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				writeSCPError(writer, err)
				return false, err
			}
			if err := writeSCPAck(writer); err != nil {
				file.Close()
				return false, err
			}
			_, copyErr := io.CopyN(file, reader, size)
			closeErr := file.Close()
			if copyErr != nil {
				return false, copyErr
			}
			if closeErr != nil {
				return false, closeErr
			}
			terminator, err := reader.ReadByte()
			if err != nil || terminator != 0 {
				return false, errors.New("invalid SCP file terminator")
			}
			_ = os.Chmod(destination, mode)
			if pendingTimes.set {
				_ = os.Chtimes(destination, pendingTimes.atime, pendingTimes.mtime)
			}
			if err := writeSCPAck(writer); err != nil {
				return false, err
			}
		}
		pendingTimes = scpTimes{}
		received = true
		if !targetIsDir {
			return false, nil
		}
	}
}
