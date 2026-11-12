package clipboard

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func Copy(text string) error {
	var cmd *exec.Cmd
	// 按运行平台选择系统剪贴板命令。
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = windowsClipboardCommand()
	case "linux":
		switch {
		case os.Getenv("WSL_DISTRO_NAME") != "":
			cmd = windowsClipboardCommand()
		case os.Getenv("WAYLAND_DISPLAY") != "":
			cmd = exec.Command("wl-copy")
		case os.Getenv("DISPLAY") != "":
			if _, err := exec.LookPath("xclip"); err == nil {
				cmd = exec.Command("xclip", "-selection", "clipboard")
			} else if _, err := exec.LookPath("xsel"); err == nil {
				cmd = exec.Command("xsel", "--clipboard", "--input")
			} else {
				return fmt.Errorf("需要安装 xclip 或 xsel")
			}
		default:
			return fmt.Errorf("当前 Linux 环境没有可用的桌面剪贴板")
		}
	default:
		return fmt.Errorf("暂不支持 %s 的剪贴板", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写入剪贴板失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func windowsClipboardCommand() *exec.Cmd {
	// 回复经标准输入传递，不拼进脚本；显式以 UTF-8 解码以保留中文。
	const script = `
$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
$text = [Console]::In.ReadToEnd()
Set-Clipboard -Value $text
`
	return exec.Command(
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-Command",
		script,
	)
}
