//go:build linux

package demolaunch

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const oPath = 0x200000 // Linux O_PATH; the frozen syscall package omits it on some architectures.
type fileID struct {
	dev, ino       uint64
	uid, gid, mode uint32
	links          uint64
	size           int64
}

func identity(f *os.File) (fileID, error) {
	var s syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &s); err != nil {
		return fileID{}, err
	}
	return fileID{uint64(s.Dev), s.Ino, s.Uid, s.Gid, s.Mode, uint64(s.Nlink), s.Size}, nil
}
func (s fileID) same(t fileID) bool { return s.dev == t.dev && s.ino == t.ino }
func (s fileID) attrs(t fileID) bool {
	return s.same(t) && s.uid == t.uid && s.gid == t.gid && s.mode == t.mode && s.links == t.links
}
func openAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

type directory struct {
	path  string
	owner uint32
	files []*os.File
	ids   []fileID
}

func (d *directory) close() {
	if d != nil {
		for _, f := range d.files {
			_ = f.Close()
		}
		d.files = nil
	}
}
func (d *directory) last() *os.File { return d.files[len(d.files)-1] }
func pinDirectory(name string, owner uint32) (_ *directory, err error) {
	if !validPath(name, 4095) {
		return nil, ErrIdentity
	}
	d := &directory{path: name, owner: owner}
	defer func() {
		if err != nil {
			d.close()
		}
	}()
	fd, e := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	d.files = append(d.files, os.NewFile(uintptr(fd), "/"))
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for i := 0; i <= len(parts); i++ {
		s, e := identity(d.last())
		if e != nil {
			return nil, e
		}
		if s.mode&syscall.S_IFMT != syscall.S_IFDIR || s.uid != 0 && s.uid != owner || s.mode&0022 != 0 {
			return nil, ErrIdentity
		}
		d.ids = append(d.ids, s)
		if i == len(parts) {
			break
		}
		f, e := openAt(d.last(), parts[i], oPath|syscall.O_DIRECTORY, 0)
		if e != nil {
			return nil, e
		}
		d.files = append(d.files, f)
	}
	return d, nil
}
func (d *directory) recheck() error {
	now, err := pinDirectory(d.path, d.owner)
	if err != nil {
		return errors.Join(ErrIdentity, err)
	}
	defer now.close()
	if len(now.ids) != len(d.ids) {
		return ErrIdentity
	}
	for i, s := range d.ids {
		if !s.same(now.ids[i]) || s.uid != now.ids[i].uid || s.gid != now.ids[i].gid || s.mode != now.ids[i].mode {
			return ErrIdentity
		}
	}
	return nil
}
func principalPin(p principal) (*directory, error) {
	d, err := pinDirectory(p.Dir, p.UID)
	if err != nil {
		return nil, errors.Join(ErrIdentity, err)
	}
	s := d.ids[len(d.ids)-1]
	if s.dev != p.Dev || s.ino != p.Ino || s.uid != p.UID || s.mode&07777 != 0700 {
		d.close()
		return nil, ErrIdentity
	}
	return d, nil
}
func endpointPin(c commonConfig) (*directory, error) {
	d, err := pinDirectory(endpointDir, c.ServiceUID)
	if err != nil {
		return nil, errors.Join(ErrIdentity, err)
	}
	s := d.ids[len(d.ids)-1]
	if s.dev != c.EndpointDev || s.ino != c.EndpointIno || s.uid != c.ServiceUID || s.gid != c.ServiceGID || s.mode&07777 != 0711 {
		d.close()
		return nil, ErrIdentity
	}
	return d, nil
}
func atIdentity(d *directory, name string) (fileID, error) {
	f, err := openAt(d.last(), name, oPath, 0)
	if err != nil {
		return fileID{}, err
	}
	defer f.Close()
	return identity(f)
}
func descriptorPath(d *directory, name string) string {
	return "/proc/self/fd/" + strconv.Itoa(int(d.last().Fd())) + "/" + name
}
func sourceSnapshot(name string, server bool) ([]byte, error) {
	mode, gid, limit := uint32(0444), uint32(0), clientConfigLimit
	if server {
		mode, gid, limit = 0440, uint32(os.Getegid()), serverConfigLimit
	}
	return protectedSnapshot(name, mode, gid, limit)
}
func protectedSnapshot(name string, mode, gid uint32, limit int) ([]byte, error) {
	return protectedSnapshotAs(name, mode, gid, limit, 0)
}

// The non-root fixture seam changes only expected ownership; production pins root.
func protectedSnapshotAs(name string, mode, gid uint32, limit int, owner uint32) ([]byte, error) {
	return protectedRead(name, mode, gid, limit, owner, readSnapshot)
}
func protectedRead(name string, mode, gid uint32, limit int, owner uint32, read func(io.Reader, int) ([]byte, error)) ([]byte, error) {
	if !validPath(name, 4095) {
		return nil, ErrConfig
	}
	cut := strings.LastIndex(name, "/")
	parent := name[:cut]
	if parent == "" {
		parent = "/"
	}
	// Root itself is a valid ancestor, although it cannot be a principal pin.
	var d *directory
	var err error
	if parent == "/" {
		fd, e := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if e != nil {
			return nil, e
		}
		f := os.NewFile(uintptr(fd), "/")
		s, e := identity(f)
		if e != nil || s.uid != 0 || s.mode&0022 != 0 {
			f.Close()
			return nil, ErrConfig
		}
		d = &directory{path: "/", files: []*os.File{f}, ids: []fileID{s}}
	} else {
		d, err = pinDirectory(parent, owner)
		if err != nil {
			return nil, errors.Join(ErrConfig, err)
		}
	}
	defer d.close()
	f, err := openAt(d.last(), name[cut+1:], syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.Join(ErrConfig, err)
	}
	defer f.Close()
	before, err := identity(f)
	if err != nil {
		return nil, err
	}
	if !snapshotMetadataAs(before, mode, gid, owner) {
		return nil, ErrConfig
	}
	b, err := read(f, limit)
	if err != nil {
		return nil, err
	}
	after, err := identity(f)
	if err != nil {
		return nil, err
	}
	named, err := atIdentity(d, name[cut+1:])
	if err != nil {
		return nil, err
	}
	if !before.attrs(after) || before.size != after.size || !after.attrs(named) || after.size != int64(len(b)) {
		return nil, ErrConfig
	}
	if parent != "/" {
		if err = d.recheck(); err != nil {
			return nil, err
		}
	}
	return b, nil
}
func groupMember(g uint32) (bool, error) {
	if uint32(os.Getegid()) == g {
		return true, nil
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false, err
	}
	for _, v := range groups {
		if uint32(v) == g {
			return true, nil
		}
	}
	return false, nil
}
func runtimeConfig(c commonConfig, uid uint32, server bool) error {
	if uint32(os.Getuid()) != uid || uint32(os.Geteuid()) != uid {
		return ErrIdentity
	}
	if server && (uint32(os.Getgid()) != c.ServiceGID || uint32(os.Getegid()) != c.ServiceGID) {
		return ErrIdentity
	}
	ok, err := groupMember(c.ConnectGID)
	if err != nil || !ok {
		return errors.Join(ErrIdentity, err)
	}
	if !server {
		member, err := groupMember(c.ServiceGID)
		if err != nil || member {
			return errors.Join(ErrIdentity, err)
		}
	}
	return nil
}

func snapshotMetadata(id fileID, mode, gid uint32) bool {
	return snapshotMetadataAs(id, mode, gid, 0)
}
func snapshotMetadataAs(id fileID, mode, gid, owner uint32) bool {
	return id.mode&syscall.S_IFMT == syscall.S_IFREG && id.uid == owner && id.gid == gid && id.mode&07777 == mode && id.links == 1
}
