package main

import (
	"archive/zip"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

//go:embed *
var sourceCode embed.FS

func main() {
	outputFilename := ""
	flag.StringVar(&outputFilename, "o", outputFilename, "output zip file")

	printSourceCode := false
	flag.BoolVar(&printSourceCode, "sauce", printSourceCode, "print source code")

	fileGlobs := []string{"*.mp3"}
	flag.Func("g",
		"file globs to append int output archive. Default values: "+strings.Join(fileGlobs, ", "),
		func(pattern string) error {
			_, err := filepath.Match(pattern, "")
			if err != nil {
				return err
			}

			fileGlobs = append(fileGlobs, pattern)
			return nil
		})

	done := func() {}
	flag.Func("cpu-profile", "enable pprof for CPU and write to specified file",
		func(filename string) error {
			f, err := os.Create(filename)
			if err != nil {
				return err
			}
			done = func() {
				pprof.StopCPUProfile()
				_ = f.Close()
			}
			return pprof.StartCPUProfile(f)
		})

	flag.Parse()

	defer done()

	if printSourceCode {
		sauce()
		return
	}

	dirs := flag.Args()

	if len(dirs) == 0 {
		panic("at least one book dir must be defined")
	}

	output, errOutput := os.OpenFile(outputFilename, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if errOutput != nil {
		panic("creating output archive: " + errOutput.Error())
	}
	defer output.Close()

	archive := zip.NewWriter(output)
	defer archive.Close()

	p := newProcessor()

	if err := p.process(archive, dirs, fileGlobs); err != nil {
		panic("processing dirs: " + err.Error())
	}
}

type fileRecord struct {
	path, name string
}

var flattenPath = strings.NewReplacer(
	string([]rune{filepath.Separator}), "_",
).Replace

var errNoFilesFound = errors.New("no files found")

func searchRecords(dir string, fsys fs.FS, fileGlobs []string) ([]fileRecord, error) {
	found := []fileRecord{}

	errWalk := fs.WalkDir(fsys, ".",
		func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			for _, pattern := range fileGlobs {
				ok, _ := filepath.Match(pattern, path)
				if ok {
					name := sanitizeDirPrefix(dir) + flattenPath(path)
					log.Printf("found file %q -> %q", path, name)
					found = append(found, fileRecord{
						name: name,
						path: filepath.Join(dir, path),
					})
					return nil
				}
			}
			return nil
		})
	if errWalk != nil {
		return nil, fmt.Errorf("walking dir: %w", errWalk)
	}

	if len(found) == 0 {
		return nil, errNoFilesFound
	}

	return found, nil
}

func sortFileRecords(records []fileRecord) {
	slices.SortStableFunc(records, func(a, b fileRecord) int {
		if a == b {
			return 0
		}
		if naturalLess(a.name, b.name) {
			return -1
		}
		return 1
	})

}

type processor struct {
	bar *mpb.Progress
}

func newProcessor() *processor {
	return &processor{
		bar: mpb.New(),
	}
}

func (p *processor) process(archive *zip.Writer, dirs, fileGlobs []string) error {
	for _, dir := range dirs {
		if err := p.processDir(archive, dir, fileGlobs); err != nil {
			return fmt.Errorf("dir %q: %w", dir, err)
		}
	}

	p.bar.Wait()

	return nil
}

func (p *processor) processDir(archive *zip.Writer, dir string, fileGlobs []string) error {
	fsys := os.DirFS(dir)
	found, errFind := searchRecords(dir, fsys, fileGlobs)
	if errFind != nil {
		return fmt.Errorf("searching files: %w", errFind)
	}

	sortFileRecords(found)

	bar := p.bar.AddBar(int64(len(found)),
		mpb.PrependDecorators(
			decor.Name(dir),
			decor.Percentage(decor.WCSyncSpace),
			decor.OnComplete(
				decor.Spinner(nil, decor.WCSyncSpace), "done",
			),
		),
	)

	for _, record := range found {
		wr, errCreate := archive.CreateHeader(&zip.FileHeader{
			Name:    record.name,
			Comment: record.path,
		})
		if errCreate != nil {
			return fmt.Errorf("creating zip file record: %w", errCreate)
		}

		if err := p.copyFileTo(wr, record.path); err != nil {
			return fmt.Errorf("writing file to archive: %w", err)
		}
		bar.Increment()
	}

	return nil
}

func (p *processor) copyFileTo(dst io.Writer, filename string) error {
	file, errFile := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0600)
	if errFile != nil {
		return fmt.Errorf("unable to open file %q: %w", filename, errFile)
	}
	defer file.Close()

	info, errInfo := file.Stat()
	if errInfo != nil {
		return fmt.Errorf("unable to open file %q: %w", filename, errFile)
	}

	bar := p.bar.AddBar(info.Size(),
		mpb.PrependDecorators(
			decor.Name(file.Name()),
			decor.Counters(decor.SizeB1024(0), " % .1f / % .1f"),
			decor.Percentage(decor.WCSyncSpace),
		))

	progress := bar.ProxyWriter(dst)
	defer progress.Close()

	_, errCopy := io.Copy(progress, file)
	if errCopy != nil {
		return fmt.Errorf("unable to write file %q: %w", filename, errCopy)
	}

	bar.Wait()

	return nil
}

// MIT License
// Copyright (c) 2013 Dan Kirkwood
// https://github.com/dangogh/naturally
func naturalLess(strA, strB string) bool {
	for {
		// get chars up to 1st digit
		posA := strings.IndexFunc(strA, unicode.IsDigit)
		posB := strings.IndexFunc(strB, unicode.IsDigit)

		if posA == -1 {
			// no digits in A
			if posB == -1 {
				// or B -- straight string compare
				return strA < strB
			}
			return false // B is Less
		} else if posB == -1 {
			return true // A is Less
		}
		subA, subB := strA[:posA], strB[:posB]
		if subA != subB {
			return subA < subB
		}
		strA, strB = strA[posA:], strB[posB:]

		// get chars up to 1st non-digit
		posA = strings.IndexFunc(strA, isNonDigit)
		posB = strings.IndexFunc(strB, isNonDigit)
		if posA == -1 {
			// no non-digits in A - allow numeric compare
			//fmt.Println(posA, " pos in ", strA)
			posA = len(strA)
		}
		if posB == -1 {
			// no non-digits in B - allow numeric compare
			posB = len(strB)
		}

		// grab numeric part of each
		valA, err := strconv.Atoi(strA[:posA])
		if err != nil {
			panic(fmt.Sprintf("Can't convert %s to a number", strA[:posA]))
		}
		valB, err := strconv.Atoi(strB[:posB])
		if err != nil {
			panic(fmt.Sprintf("Can't convert %s to a number", strA[:posA]))
		}
		if valA != valB {
			return valA < valB
		}
		if posA != posB {
			return posA < posB
		}
		if posA >= len(strA) || posB >= len(strB) {
			// should only happen if strings equal
			return true
		}
		strA, strB = strA[posA:], strB[posB:]
	}
}

func isNonDigit(ch rune) bool {
	return !unicode.IsDigit(ch)
}

func sanitizeDirPrefix(dir string) string {
	dir = filepath.Base(dir)
	dir = filepath.Clean(dir)
	if dir == "." {
		return ""
	}

	return dir + "_"
}

func sauce() {
	err := fs.WalkDir(sourceCode, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		fmt.Printf("--- %s ---\n\n", path)

		data, errFile := fs.ReadFile(sourceCode, path)
		if errFile != nil {
			return errFile
		}

		_, errWrite := os.Stdout.Write(data)
		if errWrite != nil {
			return errWrite
		}

		return nil
	})
	if err != nil {
		panic("print souce code: " + err.Error())
	}
}
