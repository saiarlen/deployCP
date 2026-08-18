package restrictedtransfer

import (
	"bytes"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestBubblewrapShellOnlyMountsPlatformHomeWritable(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "platforms", "sites", "example")
	runtimeRoot := filepath.Join(root, "core", "storage", "runtimes")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	args := buildBubblewrapArgs(&user.User{Username: "siteuser"}, 2001, 2001, []string{"2001", "2201"}, home, runtimeRoot)
	joined := strings.Join(args, " ")
	for _, want := range append([]string{
		"--dir /etc",
		"--bind " + home + " " + home,
		"--ro-bind " + runtimeRoot + " " + runtimeRoot,
		"/usr/bin/setpriv --reuid=2001 --regid=2001 --groups=2001,2201",
		"--cap-drop ALL --cap-add CAP_SETUID --cap-add CAP_SETGID",
		"--symlink ../run /var/run",
	}, prefixedDirArgs(sandboxHomeParents(home))...) {
		if !strings.Contains(joined, want) {
			t.Fatalf("sandbox arguments do not contain %q:\n%s", want, joined)
		}
	}
	for _, parent := range sandboxHomeParents(home) {
		if strings.Index(joined, "--dir "+parent) > strings.Index(joined, "--bind "+home+" "+home) {
			t.Fatalf("sandbox home ancestor %s must be created before the home bind:\n%s", parent, joined)
		}
		if !strings.Contains(joined, "--chmod 0755 "+parent) {
			t.Fatalf("sandbox home ancestor %s must be explicitly traversable:\n%s", parent, joined)
		}
	}
	if strings.Index(joined, "--dir /etc") > strings.Index(joined, "--ro-bind /etc/passwd /etc/passwd") {
		t.Fatalf("sandbox /etc must be created before individual account-file binds:\n%s", joined)
	}
	if strings.Contains(joined, "--bind / /") || strings.Contains(joined, "--ro-bind / /") {
		t.Fatalf("sandbox must not expose the host root: %s", joined)
	}
	if strings.Contains(joined, "--bounding-set=-all") || strings.Contains(joined, "--no-new-privs") {
		t.Fatalf("sandbox must preserve the narrowly scoped sudo service-control path: %s", joined)
	}
	if strings.Contains(joined, "--init-groups") {
		t.Fatalf("sandbox must pass host-resolved supplementary groups directly: %s", joined)
	}
}

func TestSandboxHomeParentsIncludesEveryAncestorInOrder(t *testing.T) {
	got := sandboxHomeParents("/home/deploycp/platforms/sites/example.test")
	want := []string{"/home", "/home/deploycp", "/home/deploycp/platforms", "/home/deploycp/platforms/sites"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox home parents = %#v, want %#v", got, want)
	}
}

func prefixedDirArgs(paths []string) []string {
	args := make([]string, 0, len(paths))
	for _, path := range paths {
		args = append(args, "--dir "+path)
	}
	return args
}

func TestBubblewrapShellClearsSupplementaryGroupsWhenNoneExist(t *testing.T) {
	args := buildBubblewrapArgs(&user.User{Username: "siteuser"}, 2001, 2001, nil, "/home/siteuser", "/srv/runtimes")
	if !strings.Contains(strings.Join(args, " "), "--clear-groups") {
		t.Fatalf("sandbox must clear supplementary groups when the account has none: %s", strings.Join(args, " "))
	}
}

func TestNormalizedGroupIDsFiltersInvalidValuesAndDuplicates(t *testing.T) {
	got := normalizedGroupIDs([]string{"2001", "invalid", "2201", "2001", "-1"})
	want := []string{"2001", "2201"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized group IDs = %#v, want %#v", got, want)
	}
}

func TestRuntimeControlRequestAllowsOnlySafeOwnServiceActions(t *testing.T) {
	for _, request := range []runtimeControlRequest{{Action: "restart", Unit: "deploycp-app-example-com"}, {Action: "status", Unit: "deploycp-app-example-com"}} {
		if !validRuntimeControlRequest(request) {
			t.Fatalf("request %#v should be allowed", request)
		}
	}
	for _, request := range []runtimeControlRequest{{Action: "enable", Unit: "deploycp-app-example-com"}, {Action: "restart", Unit: "ssh.service"}, {Action: "restart", Unit: "deploycp-app-x;id"}} {
		if validRuntimeControlRequest(request) {
			t.Fatalf("request %#v should be rejected", request)
		}
	}
}

func TestSplitShellCommandPreservesQuotedSCPPath(t *testing.T) {
	got, err := splitShellCommand(`scp -pt 'folder/file with spaces.txt'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"scp", "-pt", "folder/file with spaces.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
}

func TestChrootSCPPathCannotEscapeRoot(t *testing.T) {
	for _, input := range []string{"/etc/passwd", "../../etc/passwd", "/srv/sites/example/../../other"} {
		got := chrootSCPPath(input, "/srv/sites/example")
		if !strings.HasPrefix(got, "/") || strings.Contains(got, "..") {
			t.Fatalf("unsafe chroot path %q from %q", got, input)
		}
	}
	if got := chrootSCPPath("/srv/sites/example/htdocs/app.txt", "/srv/sites/example"); got != "/htdocs/app.txt" {
		t.Fatalf("home-relative path = %q", got)
	}
}

func TestReceiveSCPSupportsFilenameWithSpaces(t *testing.T) {
	target := t.TempDir()
	input := bytes.NewBufferString("C0644 5 file with spaces.txt\nhello\x00")
	var output bytes.Buffer
	err := receiveSCP(scpServerOptions{mode: 't', targetDir: true, path: target}, input, &output)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "file with spaces.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q", content)
	}
	if got := output.Bytes(); !bytes.Equal(got, []byte{0, 0, 0}) {
		t.Fatalf("acks = %v", got)
	}
}

func TestReceiveSCPRecursiveDirectory(t *testing.T) {
	target := t.TempDir()
	input := bytes.NewBufferString("D0755 0 folder\nC0644 6 nested.txt\nnested\x00E\n")
	var output bytes.Buffer
	err := receiveSCP(scpServerOptions{mode: 't', targetDir: true, recursive: true, path: target}, input, &output)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "folder", "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "nested" {
		t.Fatalf("content = %q", content)
	}
}

func TestSendSCPFileProtocol(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "download.txt")
	if err := os.WriteFile(filePath, []byte("download"), 0o640); err != nil {
		t.Fatal(err)
	}
	input := bytes.NewReader([]byte{0, 0, 0})
	var output bytes.Buffer
	if err := sendSCP(scpServerOptions{mode: 'f', path: filePath}, input, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.HasPrefix(got, "C0640 8 download.txt\n") || !strings.Contains(got, "download\x00") {
		t.Fatalf("unexpected SCP source protocol: %q", got)
	}
}

func TestVirtualPathUsesChrootNamespace(t *testing.T) {
	if got := filepath.ToSlash(virtualPath("../../etc/passwd")); got != "/etc/passwd" {
		t.Fatalf("virtual path = %q", got)
	}
}

func TestSFTPServerConfinesTraversalToVirtualRoot(t *testing.T) {
	root := t.TempDir()
	serverConn, clientConn := net.Pipe()
	fs := &chrootFS{originalHome: "/srv/sites/example", root: root}
	server := sftp.NewRequestServer(serverConn, sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve() }()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	file, err := client.Create("../../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("confined")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "outside.txt")); err != nil {
		t.Fatalf("traversal path was not confined inside root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("SFTP traversal created a file outside the virtual root: %v", err)
	}

	_ = client.Close()
	_ = server.Close()
	_ = clientConn.Close()
	<-serverDone
}
