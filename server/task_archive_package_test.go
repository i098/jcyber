package server

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	pgdb "pta/db"
)

func TestTaskArchivePackageFilesRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	taskID := "42"
	explorationID := int64(73)
	taskFile := filepath.Join(dataDir, "tasks", taskID, "uploads", "evidence.txt")
	transcriptFile := filepath.Join(dataDir, "transcripts", "exp73-worker-1.jsonl")
	unrelatedTranscript := filepath.Join(dataDir, "transcripts", "exp74-worker-1.jsonl")
	for path, body := range map[string]string{
		taskFile:            "task evidence",
		transcriptFile:      "transcript",
		unrelatedTranscript: "leave me hot",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stage, err := stageTaskArchiveFiles(dataDir, 1, taskID, explorationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(taskFile); !os.IsNotExist(err) {
		t.Fatalf("task file still hot after staging: %v", err)
	}
	if _, err := os.Stat(transcriptFile); !os.IsNotExist(err) {
		t.Fatalf("task transcript still hot after staging: %v", err)
	}
	if _, err := os.Stat(unrelatedTranscript); err != nil {
		t.Fatalf("unrelated transcript was staged: %v", err)
	}

	archivePath := taskArchivePath(dataDir, 1, taskID)
	streamPath := filepath.Join(stage.payload, filepath.FromSlash(pgdb.TaskArchiveLLMRecordsPath))
	if err := os.MkdirAll(filepath.Dir(streamPath), archiveDirMode); err != nil {
		t.Fatal(err)
	}
	streamBody := []byte("{\"id\":1,\"raw_request\":\"large\"}\n")
	if err := os.WriteFile(streamPath, streamBody, archiveFileMode); err != nil {
		t.Fatal(err)
	}
	snapshot := &pgdb.TaskArchiveSnapshot{
		FormatVersion: pgdb.TaskArchiveFormatVersion, TaskID: 42, ExplorationID: explorationID,
		StreamedTables: map[string]string{"llm_records": pgdb.TaskArchiveLLMRecordsPath},
	}
	original, compressed, checksum, err := writeTaskArchivePackage(archivePath, stage.payload, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if original == 0 || compressed == 0 || checksum == "" {
		t.Fatalf("invalid package metrics original=%d compressed=%d checksum=%q", original, compressed, checksum)
	}
	if err := stage.commit(); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(dataDir, "restore")
	if err := extractTaskArchivePackage(archivePath, checksum, extracted); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(extracted, filepath.FromSlash(pgdb.TaskArchiveLLMRecordsPath))); err != nil || !bytes.Equal(got, streamBody) {
		t.Fatalf("streamed LLM archive payload=%q err=%v", got, err)
	}
	installed, err := installTaskArchiveFiles(dataDir, extracted, taskID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.commit(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{taskFile: "task evidence", transcriptFile: "transcript"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("restored %s = %q, %v; want %q", path, got, err, want)
		}
	}
	if err := extractTaskArchivePackage(archivePath, "deadbeef", filepath.Join(dataDir, "bad-checksum")); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}
func TestTaskArchivePackageReportsCompressionProgress(t *testing.T) {
	payload := t.TempDir()
	big := make([]byte, 8192)
	for i, body := range [][]byte{[]byte("alpha"), big} {
		if err := os.WriteFile(filepath.Join(payload, "f"+string(rune('0'+i))+".bin"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := int64(5 + len(big))
	var calls int
	var done, total int64
	original, _, _, err := writeTaskArchivePackage(filepath.Join(t.TempDir(), "p.tar.zst"), payload,
		&pgdb.TaskArchiveSnapshot{FormatVersion: pgdb.TaskArchiveFormatVersion}, func(soFar, all int64) {
			calls++
			if soFar < done {
				t.Fatalf("progress went backwards: %d after %d", soFar, done)
			}
			done, total = soFar, all
		})
	if err != nil {
		t.Fatal(err)
	}
	// The final report covers everything that goes into the archive (file bodies
	// plus manifest.json), so it must match the returned original size.
	if calls == 0 || done != original || total != original || done <= files {
		t.Fatalf("progress calls=%d done=%d total=%d original=%d (files=%d)", calls, done, total, original, files)
	}
}

func TestTaskArchivePackageSkipsSymlink(t *testing.T) {
	dataDir := t.TempDir()
	payload := filepath.Join(dataDir, "payload")
	if err := os.MkdirAll(payload, archiveDirMode); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(payload, "keep.txt")
	if err := os.WriteFile(regular, []byte("keep me"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	// 工作目录里出现的符号链接应被跳过，而不是让整个归档失败。
	if err := os.Symlink(regular, filepath.Join(payload, "link.txt")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	archivePath := filepath.Join(dataDir, "archive.tar.zst")
	snapshot := &pgdb.TaskArchiveSnapshot{FormatVersion: pgdb.TaskArchiveFormatVersion, TaskID: 42}
	_, _, checksum, err := writeTaskArchivePackage(archivePath, payload, snapshot, nil)
	if err != nil {
		t.Fatalf("archive should skip symlink, not fail: %v", err)
	}
	extracted := filepath.Join(dataDir, "restore")
	if err := extractTaskArchivePackage(archivePath, checksum, extracted); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(extracted, "keep.txt")); err != nil || string(got) != "keep me" {
		t.Fatalf("regular file not archived: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(extracted, "link.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink should have been skipped, but link.txt exists: %v", err)
	}
}

func TestTaskArchivePackageSkipsSocket(t *testing.T) {
	// sun_path 在 macOS 上只有 104 字节，t.TempDir() 的路径太长，socket 建不出来。
	dataDir, err := os.MkdirTemp("", "jcs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	payload := filepath.Join(dataDir, "p")
	if err := os.MkdirAll(payload, archiveDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "keep.txt"), []byte("keep me"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	// 任务目录里残留的 unix socket 曾让 tar.FileInfoHeader 报
	// "archive/tar: sockets not supported"，整个归档在压缩末尾失败。
	sock := filepath.Join(payload, "dev.sock")
	l, listenErr := net.Listen("unix", sock)
	if listenErr != nil {
		t.Skipf("unix sockets unsupported on this platform: %v", listenErr)
	}
	defer l.Close()

	archivePath := filepath.Join(dataDir, "archive.tar.zst")
	snapshot := &pgdb.TaskArchiveSnapshot{FormatVersion: pgdb.TaskArchiveFormatVersion, TaskID: 42}
	_, _, checksum, err := writeTaskArchivePackage(archivePath, payload, snapshot, nil)
	if err != nil {
		t.Fatalf("archive should skip the socket, not fail: %v", err)
	}
	extracted := filepath.Join(dataDir, "restore")
	if err := extractTaskArchivePackage(archivePath, checksum, extracted); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(extracted, "keep.txt")); err != nil || string(got) != "keep me" {
		t.Fatalf("regular file not archived: got=%q err=%v", got, err)
	}
}

func TestTaskArchiveStageJournalRollsBackInterruptedMoves(t *testing.T) {
	dataDir := t.TempDir()
	taskFile := filepath.Join(dataDir, "tasks", "42", "evidence.txt")
	transcriptFile := filepath.Join(dataDir, "transcripts", "exp73-worker.jsonl")
	for _, path := range []string{taskFile, transcriptFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stage, err := stageTaskArchiveFiles(dataDir, 9, "42", 73)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(stage.root, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal archiveStageJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	if len(journal.Moves) != 2 {
		t.Fatalf("journal moves=%d, want 2", len(journal.Moves))
	}
	recovered := &taskArchiveFileStage{root: stage.root, payload: stage.payload, journal: journal}
	if err := recovered.rollback(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{taskFile, transcriptFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("interrupted archive did not restore %s: %v", path, err)
		}
	}
}

func TestTaskArchiveRestoreJournalRollsBackInterruptedInstall(t *testing.T) {
	dataDir := t.TempDir()
	extracted := filepath.Join(dataDir, "archives", "tasks", ".restore", "11-test")
	source := filepath.Join(extracted, "files", "tasks", "42", "evidence.txt")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := installTaskArchiveFiles(dataDir, extracted, "42", 11)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dataDir, "tasks", "42", "evidence.txt")
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(extracted, "restore-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal archiveRestoreJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.ArchiveID != 11 || len(journal.Moves) != 1 {
		t.Fatalf("unexpected restore journal: %+v", journal)
	}
	recovered := &taskArchiveRestoreFiles{extracted: extracted, moves: journal.Moves}
	if err := recovered.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("hot destination remains after restore rollback: %v", err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "evidence" {
		t.Fatalf("restore source=%q err=%v", got, err)
	}
}

func TestTaskArchiveDeletePackageCanResumeFromStagedPath(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := taskArchivePath(dataDir, 21, "42")
	if err := os.MkdirAll(filepath.Dir(archivePath), archiveDirMode); err != nil {
		t.Fatal(err)
	}
	staged := archivePath + ".deleting-21"
	if err := os.WriteFile(staged, []byte("archive"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	got, moved, err := stageTaskArchivePackageDelete(archivePath, 21)
	if err != nil || !moved || got != staged {
		t.Fatalf("resume staged package path=%q moved=%v err=%v", got, moved, err)
	}
}

func TestTaskArchivePackageRejectsTraversal(t *testing.T) {
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(encoder)
	content := []byte("escape")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "malicious.tar.zst")
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := extractTaskArchivePackage(path, "", root); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside destination: %v", err)
	}
}
