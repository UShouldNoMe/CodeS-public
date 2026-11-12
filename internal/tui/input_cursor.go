package tui

// 输出适配层：包装标准输出，为 Windows 输入法设置真实光标位置。
// 框架按照自身记录绘制；包装器在帧末将光标移至输入处。
// 备用屏幕中的帧以绝对光标定位结尾，下一帧也会重新定位光标。
// 这里的注释说明当前机制和假设，不表示终端缩放效果已验证。

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// inputCursorWriter decorates Bubble Tea's final write for a rendered frame.
// Bubble Tea v1 leaves the terminal cursor at the start of the status row;
// Windows IMEs use that physical cursor as the anchor for pre-edit text. We
// therefore restore it to the end of the editable prompt.
type inputCursorWriter struct {
	out       *os.File
	altScreen bool

	mu      sync.RWMutex
	enabled bool
	column  int
	rowsUp  int
	// restoreRows tracks the last physical move independently of the next View.
	restoreRows int
}

// 构造时只包装文件，暂不移动光标。
func newInputCursorWriter(out *os.File, altScreen bool) *inputCursorWriter {
	return &inputCursorWriter{out: out, altScreen: altScreen}
}

// These methods preserve the terminal-file contract Bubble Tea uses to obtain
// window dimensions. In particular, the wrapper must not make os.Stdout look
// like an ordinary writer, otherwise Bubble Tea never sends WindowSizeMsg.
// 转发文件读取能力，与 Fd 一起保留底层终端文件的接口特征。
func (w *inputCursorWriter) Read(p []byte) (int, error) { return w.out.Read(p) }

// 不关闭标准输出，文件生命周期由调用方管理。
func (w *inputCursorWriter) Close() error { return nil } // CodeS owns os.Stdout.

// 暴露真实文件描述符，让框架仍能识别终端并查询尺寸。
func (w *inputCursorWriter) Fd() uintptr { return w.out.Fd() }

// 加锁记录下一次输出使用的光标配置，此时不发送控制序列。
func (w *inputCursorWriter) set(enabled bool, column, rowsUp int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enabled = enabled
	w.column = column
	w.rowsUp = rowsUp
}

// 先按记录的行数尝试恢复光标，再写渲染器数据。
// 根据缓冲区模式识别帧结尾，把真实光标移到输入处。
func (w *inputCursorWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	// Restore the renderer's status-row anchor before any frame or control
	// write, including when the next View has disabled the input cursor.
	if w.restoreRows > 0 {
		if _, err := io.WriteString(w.out, ansi.CursorDown(w.restoreRows)+"\r"); err != nil {
			return 0, err
		}
		w.restoreRows = 0
	}
	n, err := w.out.Write(p)
	if err != nil {
		return n, err
	}

	enabled, column, rowsUp := w.enabled, w.column, w.rowsUp
	// 普通缓冲区的帧以回车结尾；备用屏幕的帧以状态行的绝对定位结尾。
	frameEnd := bytes.HasSuffix(p, []byte("\r"))
	if w.altScreen {
		frameEnd = bytes.HasSuffix(p, []byte(";H"))
	}
	if !enabled || !frameEnd {
		return n, nil
	}
	if _, err := io.WriteString(w.out, ansi.ShowCursor+ansi.CursorUp(rowsUp)+ansi.CursorHorizontalAbsolute(column)); err != nil {
		return n, err
	}
	if !w.altScreen {
		w.restoreRows = rowsUp
	}
	return n, nil
}
