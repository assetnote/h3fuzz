package input

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const maxWordlistLineSize = 512 * 1024

const maxWordlistArenaPreallocate = 64 * 1024 * 1024
const maxWordlistOffsetPreallocate = 1024 * 1024

var extensionMarkerPattern = regexp.MustCompile(`(?i)%ext%`)

// wordlistStorage keeps regular-file entries in two pointer-free backing
// arrays. ends32 contains the exclusive end of each entry in arena; the start
// is the preceding end (or zero for the first entry). Very large expanded
// wordlists promote the offsets to uint64 instead of truncating them.
//
// Stdin deliberately retains the historical [][]byte representation. Unlike
// a regular file it has no size hint and callers may depend on each streamed
// value owning an independent allocation.
type wordlistStorage struct {
	arena     []byte
	ends32    []uint32
	ends64    []uint64
	streamed  [][]byte
	fileArena bool
}

func newRegularFileWordlistStorage(size int64) wordlistStorage {
	storage := wordlistStorage{fileArena: true}
	if size <= 0 {
		return storage
	}
	if size > maxWordlistArenaPreallocate {
		size = maxWordlistArenaPreallocate
	}
	storage.arena = make([]byte, 0, int(size))
	offsetCapacity := int(size) / 16
	if offsetCapacity > maxWordlistOffsetPreallocate {
		offsetCapacity = maxWordlistOffsetPreallocate
	}
	storage.ends32 = make([]uint32, 0, offsetCapacity)
	return storage
}

func (s *wordlistStorage) append(value ...[]byte) {
	if !s.fileArena {
		length := 0
		for _, part := range value {
			length += len(part)
		}
		entry := make([]byte, 0, length)
		for _, part := range value {
			entry = append(entry, part...)
		}
		s.streamed = append(s.streamed, entry)
		return
	}

	for _, part := range value {
		s.arena = append(s.arena, part...)
	}
	end := uint64(len(s.arena))
	if s.ends64 != nil {
		s.ends64 = append(s.ends64, end)
		return
	}
	if end <= math.MaxUint32 {
		s.ends32 = append(s.ends32, uint32(end))
		return
	}

	s.ends64 = make([]uint64, len(s.ends32), cap(s.ends32)+1)
	for index, offset := range s.ends32 {
		s.ends64[index] = uint64(offset)
	}
	s.ends32 = nil
	s.ends64 = append(s.ends64, end)
}

func (s *wordlistStorage) total() int {
	if !s.fileArena {
		return len(s.streamed)
	}
	if s.ends64 != nil {
		return len(s.ends64)
	}
	return len(s.ends32)
}

func (s *wordlistStorage) value(position int) []byte {
	if !s.fileArena {
		return s.streamed[position]
	}
	var start, end uint64
	if s.ends64 != nil {
		end = s.ends64[position]
		if position > 0 {
			start = s.ends64[position-1]
		}
	} else {
		end = uint64(s.ends32[position])
		if position > 0 {
			start = uint64(s.ends32[position-1])
		}
	}
	// Restrict capacity to the entry boundary. This preserves the historical
	// behavior where appending to a returned value cannot overwrite the next
	// wordlist entry.
	return s.arena[int(start):int(end):int(end)]
}

type WordlistInput struct {
	active   bool
	config   *ffuf.Config
	storage  wordlistStorage
	position int
	keyword  string
}

func NewWordlistInput(keyword string, value string, conf *ffuf.Config) (*WordlistInput, error) {
	var wl WordlistInput
	wl.active = true
	wl.keyword = keyword
	wl.config = conf
	wl.position = 0
	var valid bool
	var err error
	// stdin?
	if value == "-" {
		// yes
		valid = true
	} else {
		// no
		valid, err = wl.validFile(value)
	}
	if err != nil {
		return &wl, err
	}
	if valid {
		err = wl.readFile(value)
	}
	return &wl, err
}

// Position will return the current position in the input list
func (w *WordlistInput) Position() int {
	return w.position
}

// SetPosition sets the current position of the inputprovider
func (w *WordlistInput) SetPosition(pos int) {
	w.position = pos
}

// ResetPosition resets the position back to beginning of the wordlist.
func (w *WordlistInput) ResetPosition() {
	w.position = 0
}

// Keyword returns the keyword assigned to this InternalInputProvider
func (w *WordlistInput) Keyword() string {
	return w.keyword
}

// Next will return a boolean telling if there's words left in the list
func (w *WordlistInput) Next() bool {
	return w.position < w.storage.total()
}

// IncrementPosition will increment the current position in the inputprovider data slice
func (w *WordlistInput) IncrementPosition() {
	w.position += 1
}

// Value returns the value from wordlist at current cursor position
func (w *WordlistInput) Value() []byte {
	return w.storage.value(w.position)
}

// Total returns the size of wordlist
func (w *WordlistInput) Total() int {
	return w.storage.total()
}

// Active returns boolean if the inputprovider is active
func (w *WordlistInput) Active() bool {
	return w.active
}

// Enable sets the inputprovider as active
func (w *WordlistInput) Enable() {
	w.active = true
}

// Disable disables the inputprovider
func (w *WordlistInput) Disable() {
	w.active = false
}

// validFile checks that the wordlist file exists and can be read
func (w *WordlistInput) validFile(path string) (bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	f.Close()
	return true, nil
}

// readFile reads the file line by line to a byte slice
func (w *WordlistInput) readFile(path string) error {
	var file *os.File
	var storage wordlistStorage
	var err error
	if path == "-" {
		file = os.Stdin
	} else {
		file, err = os.Open(path)
		if err != nil {
			return err
		}
		if info, statErr := file.Stat(); statErr == nil {
			storage = newRegularFileWordlistStorage(info.Size())
		} else {
			storage = newRegularFileWordlistStorage(0)
		}
	}
	defer file.Close()

	var ok bool
	reader := bufio.NewScanner(file)
	// Scanner defaults to 64 KiB tokens, which rejects valid large request-body
	// payloads. Keep a finite ceiling to avoid unbounded allocation while
	// supporting the large wordlist entries reported in #557 and #567.
	// Scanner's maximum includes line framing and is otherwise effectively
	// exclusive. Reserve CRLF framing separately, then enforce the advertised
	// logical-line limit on the delimiter-stripped token below.
	reader.Buffer(nil, maxWordlistLineSize+2)
	var lineSizeErr error
	for reader.Scan() {
		line := reader.Bytes()
		if len(line) > maxWordlistLineSize {
			lineSizeErr = fmt.Errorf("wordlist entry exceeds %d-byte limit", maxWordlistLineSize)
			break
		}
		if extensionMarkerPattern.Match(line) && strings.Contains(w.config.Url, w.keyword) && (!w.config.DirSearchCompat || len(w.config.Extensions) == 0) {
			text := string(line)
			return fmt.Errorf("wordlist entry %q contains %%EXT%% in a URL payload; expand it with -D -e EXT (for example, -D -e aspx)", text)
		}
		if w.config.DirSearchCompat && len(w.config.Extensions) > 0 {
			if extensionMarkerPattern.Match(line) {
				for _, ext := range w.config.Extensions {
					content := extensionMarkerPattern.ReplaceAll(line, []byte(ext))
					storage.append(content)
				}
			} else {
				if w.config.IgnoreWordlistComments {
					line, ok = stripCommentsBytes(line)
					if !ok {
						continue
					}
				}
				storage.append(line)
			}
		} else {
			if w.config.IgnoreWordlistComments {
				line, ok = stripCommentsBytes(line)
				if !ok {
					continue
				}
			}
			storage.append(line)
			if w.keyword == "FUZZ" && len(w.config.Extensions) > 0 {
				for _, ext := range w.config.Extensions {
					storage.append(line, []byte(ext))
				}
			}
		}
	}
	w.storage = storage
	if lineSizeErr != nil {
		return lineSizeErr
	}
	return reader.Err()
}

func stripCommentsBytes(text []byte) ([]byte, bool) {
	if bytes.HasPrefix(bytes.TrimLeft(text, " "), []byte("#")) {
		return nil, false
	}
	index := bytes.Index(text, []byte(" #"))
	if index == -1 {
		return text, true
	}
	return text[:index], true
}

// stripComments removes all kind of comments from the word
func stripComments(text string) (string, bool) {
	// If the line starts with a # ignoring any space on the left,
	// return blank.
	if strings.HasPrefix(strings.TrimLeft(text, " "), "#") {
		return "", false
	}

	// If the line has # later after a space, that's a comment.
	// Only send the word upto space to the routine.
	index := strings.Index(text, " #")
	if index == -1 {
		return text, true
	}
	return text[:index], true
}
