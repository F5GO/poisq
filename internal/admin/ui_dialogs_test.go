// Файл ui_dialogs_test.go защищает веб-панель от возврата системных окон браузера.
//
// Что делает этот код и для чего он нужен:
// Все подтверждения в Studio идут через модальный компонент confirmDialog. Тест читает
// встроенный фронтенд и падает, если в нём снова появился вызов confirm/alert/prompt
// или пропал сам компонент, чтобы вид и поведение диалогов не расходились.
package admin

import (
	"regexp"
	"strings"
	"testing"
)

var nativeDialogCall = regexp.MustCompile(`(^|[^.\w$])(confirm|alert|prompt)\s*\(`)

func TestEmbeddedUIHasNoNativeDialogs(t *testing.T) {
	for _, name := range []string{"ui/app.js", "ui/timeline-editor.js"} {
		data, err := embeddedUI.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if nativeDialogCall.MatchString(line) {
				t.Errorf("%s:%d uses a native browser dialog, use confirmDialog instead: %s", name, i+1, trimmed)
			}
		}
	}
}

func TestConfirmDialogComponentIsPresent(t *testing.T) {
	data, err := embeddedUI.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{"function confirmDialog(", "window.confirmDialog = confirmDialog", `"alertdialog"`, `"Escape"`} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js must contain %q", want)
		}
	}
}

func TestNativeDialogPatternDetectsViolations(t *testing.T) {
	hits := []string{`if (!confirm("x")) return;`, `alert("x")`, `x = prompt ("y")`}
	for _, h := range hits {
		if !nativeDialogCall.MatchString(h) {
			t.Errorf("pattern should match %q", h)
		}
	}
	for _, ok := range []string{`await confirmDialog({`, `obj.confirm(1)`, `reconfirm(2)`, `$confirm(3)`} {
		if nativeDialogCall.MatchString(ok) {
			t.Errorf("pattern should not match %q", ok)
		}
	}
}
