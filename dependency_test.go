package writeaheadlog

import (
	"io/ioutil"
	"os"
	"sync"

	"github.com/NebulousLabs/errors"
	"github.com/NebulousLabs/fastrand"
)

// scrambleData takes some data as input and replaces parts of it randomly with
// random data
func scrambleData(d []byte) []byte {
	randomData := fastrand.Bytes(len(d))
	scrambled := make([]byte, len(d), len(d))
	for i := 0; i < len(d); i++ {
		if fastrand.Intn(4) == 0 { // 25% chance to replace byte
			scrambled[i] = randomData[i]
		} else {
			scrambled[i] = d[i]
		}
	}
	return scrambled
}

// faultyDiskDependency implements dependencies that simulate a faulty disk.
type faultyDiskDependency struct {
	// failDenominator determines how likely it is that a write will fail,
	// defined as 1/failDenominator. Each write call increments
	// failDenominator, and it starts at 2. This means that the more calls to
	// WriteAt, the less likely the write is to fail. All calls will start
	// automatically failing after writeLimit writes.
	failDenominator uint64
	writeLimit      uint64
	failed          bool
	disabled        bool
	mu              sync.Mutex
}

// newFaultyDiskDependency creates a dependency that can be used to simulate a
// failing disk. writeLimit is the maximum number of writes the disk will
// endure before failing
func newFaultyDiskDependency(writeLimit uint64) faultyDiskDependency {
	return faultyDiskDependency{
		failDenominator: uint64(3),
		writeLimit:      writeLimit,
	}
}

func (d *faultyDiskDependency) create(path string) (file, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed {
		return nil, errors.New("failed to create file (faulty disk)")
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return d.newFaultyFile(f), nil
}

// disabled allows the caller to temporarily disable the dependency
func (d *faultyDiskDependency) disable(b bool) {
	d.mu.Lock()
	d.disabled = b
	d.mu.Unlock()
}
func (*faultyDiskDependency) disrupt(s string) bool {
	return s == "FaultyDisk"
}

// newFaultyFile creates a new faulty file around the provided file handle.
func (d *faultyDiskDependency) newFaultyFile(f *os.File) *faultyFile {
	return &faultyFile{d: d, file: f}
}
func (*faultyDiskDependency) readFile(path string) ([]byte, error) {
	return ioutil.ReadFile(path)
}
func (d *faultyDiskDependency) remove(path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.disabled {
		return os.Remove(path)
	}

	fail := fastrand.Intn(int(d.failDenominator)) == 0
	d.failDenominator++
	if fail || d.failed || d.failDenominator >= d.writeLimit {
		d.failed = true
		return nil
	}

	return os.Remove(path)
}

// reset resets the failDenominator and the failed flag of the dependency
func (d *faultyDiskDependency) reset() {
	d.mu.Lock()
	d.failDenominator = 3
	d.failed = false
	d.mu.Unlock()
}
func (d *faultyDiskDependency) openFile(path string, flag int, perm os.FileMode) (file, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return d.newFaultyFile(f), nil
}

// faultyFile implements a file that simulates a faulty disk.
type faultyFile struct {
	d    *faultyDiskDependency
	file *os.File
}

func (f *faultyFile) Read(p []byte) (int, error) {
	return f.file.Read(p)
}
func (f *faultyFile) Write(p []byte) (int, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()

	if f.d.disabled {
		return f.file.Write(p)
	}

	fail := fastrand.Intn(int(f.d.failDenominator)) == 0
	f.d.failDenominator++
	if fail || f.d.failed || f.d.failDenominator >= f.d.writeLimit {
		f.d.failed = true
		// scramble data
		return f.file.Write(scrambleData(p))
	}

	return f.file.Write(p)
}
func (f *faultyFile) Close() error { return f.file.Close() }
func (f *faultyFile) Name() string {
	return f.file.Name()
}
func (f *faultyFile) ReadAt(p []byte, off int64) (int, error) {
	return f.file.ReadAt(p, off)
}
func (f *faultyFile) WriteAt(p []byte, off int64) (int, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()

	if f.d.disabled {
		return f.file.WriteAt(p, off)
	}

	fail := fastrand.Intn(int(f.d.failDenominator)) == 0
	f.d.failDenominator++
	if fail || f.d.failed || f.d.failDenominator >= f.d.writeLimit {
		f.d.failed = true
		// scramble data
		return f.file.WriteAt(scrambleData(p), off)
	}
	return f.file.WriteAt(p, off)
}
func (f *faultyFile) Stat() (os.FileInfo, error) {
	return f.file.Stat()
}
func (f *faultyFile) Sync() error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()

	if !f.d.disabled && f.d.failed {
		return errors.New("could not write to disk (faultyDisk)")
	}
	return f.file.Sync()
}

// dependencyCommitFail corrupts the first page of a transaction when it
// is committed
type dependencyCommitFail struct {
	prodDependencies
}

func (*dependencyCommitFail) disrupt(s string) bool {
	if s == "CommitFail" {
		return true
	}
	return false
}

// dependencyRecoveryFail causes the RecoveryComplete function to fail after
// the metadata was changed to wipe
type dependencyRecoveryFail struct {
	prodDependencies
}

func (dependencyRecoveryFail) disrupt(s string) bool {
	if s == "RecoveryFail" {
		return true
	}
	if s == "UncleanShutdown" {
		return true
	}
	return false
}

// dependencyReleaseFail corrupts the first page of a transaction when it
// is released
type dependencyReleaseFail struct {
	prodDependencies
}

func (*dependencyReleaseFail) disrupt(s string) bool {
	if s == "ReleaseFail" {
		return true
	}
	return false
}

// dependencyUncleanShutdown prevnts the wal from marking the logfile as clean
// upon shutdown
type dependencyUncleanShutdown struct {
	prodDependencies
}

func (dependencyUncleanShutdown) disrupt(s string) bool {
	if s == "UncleanShutdown" {
		return true
	}
	return false
}