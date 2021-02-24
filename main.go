package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/zellyn/kooky"
	_ "github.com/zellyn/kooky/allbrowsers"
)

const (
	CommentPrefix = "#"
	OtherPrefix   = "OTHER "
)

type ConfigReport struct {
	AuthorName    string `toml:"author-name"`
	SubjectPrefix string `toml:"subject-prefix"`
	TaskPrefix    string `toml:"task-prefix"`
}

type ConfigJIRA struct {
	Host string
}

type Config struct {
	InputFile string `toml:"input-file"`
	MyEmail   string `toml:"my-email"`
	WorkEmail string `toml:"work-email"`
	SmtpHost  string `toml:"smtp-host"`
	Report    ConfigReport
	JIRA      ConfigJIRA
}

func getJIRATasks(config *ConfigJIRA) ([]string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", config.Host+"issues/?filter=40605", nil)
	if err != nil {
		return nil, err
	}

	cookies := kooky.ReadCookies(kooky.DomainHasSuffix(path.Base(config.Host)))
	if len(cookies) == 0 {
		fmt.Println("got 0 cookies")
	}
	for _, c := range cookies {
		req.Header.Add("Cookie", c.Name+"="+c.Value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jiraTasks []string
	re := regexp.MustCompile(`<a class="issue-link" data-issue-key=[^>]+>([^<]+)</a>`)
	matches := re.FindAllSubmatch(body, -1)
	for i := 0; i < len(matches); i += 2 {
		jiraTasks = append(jiraTasks, string(matches[i][1])+" "+string(matches[i+1][1]))
	}

	return jiraTasks, nil
}

func cleanInpulFile(config *Config) error {
	file, err := os.OpenFile(config.InputFile, os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil
	}

	var builder strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, CommentPrefix) {
			builder.WriteString(line)
			builder.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		file.Close()
		return err
	}
	file.Close()

	return ioutil.WriteFile(config.InputFile, []byte(builder.String()), 0644)
}

func dumpJIRATasks(config *Config, jiraTasks []string) error {
	f, err := os.OpenFile(config.InputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, jt := range jiraTasks {
		if _, err := f.WriteString(CommentPrefix + jt + "\n"); err != nil {
			return err
		}
	}

	return nil
}

func getAndDumpJIRATasks(config *Config) error {
	jiraTasks, err := getJIRATasks(&config.JIRA)
	if err != nil {
		return err
	}
	if err := cleanInpulFile(config); err != nil {
		return err
	}

	return dumpJIRATasks(config, jiraTasks)
}

func generateReport(config *Config, text, to, date string) string {
	var msg strings.Builder
	msg.WriteString("From: ")
	msg.WriteString(config.Report.AuthorName)
	msg.WriteString("<")
	msg.WriteString(config.MyEmail)
	msg.WriteString(">\nTo: ")
	msg.WriteString(to)
	msg.WriteString("\nSubject: ")
	msg.WriteString(config.Report.SubjectPrefix)
	msg.WriteString(date)
	msg.WriteString("\nMIME-Version: 1.0\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\n")
	msg.WriteString(`<ul type="disc">`)

	lines := strings.Split(strings.TrimSpace(text), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, config.Report.TaskPrefix) {
			tokens := strings.SplitN(line, " ", 2)
			msg.WriteString(`<li><b><a href="`)
			msg.WriteString(config.JIRA.Host)
			msg.WriteString("browse/")
			msg.WriteString(tokens[0])
			msg.WriteString(`">`)
			msg.WriteString(tokens[0])
			msg.WriteString("</a>")
			if len(tokens) > 1 {
				msg.WriteString(" " + tokens[1])
			}
			msg.WriteString("</b></li>")
		} else if strings.HasPrefix(line, OtherPrefix) {
			msg.WriteString("<li>")
			msg.WriteString(strings.TrimPrefix(line, OtherPrefix))
			msg.WriteString("</li>")
		} else if strings.HasPrefix(line, CommentPrefix) {
			// skip
		} else {
			msg.WriteString(`<ul type="circle"><li>`)
			msg.WriteString(line)
			msg.WriteString("</li></ul>")
		}
	}

	msg.WriteString("</ul>\n<br><br>--<br>С уважением,<br>")
	msg.WriteString(config.Report.AuthorName)

	return msg.String()
}

func sendReport(config *Config, report, to string) error {
	attrs := syscall.ProcAttr{
		Dir:   "",
		Env:   []string{},
		Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()},
		Sys:   nil,
	}
	var ws syscall.WaitStatus
	pid, err := syscall.ForkExec(
		"/bin/stty",
		[]string{"stty", "-echo"},
		&attrs,
	)
	if err != nil {
		return err
	}
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("pass> ")
	pass, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	pass = strings.TrimSpace(pass)

	pid, err = syscall.ForkExec(
		"/bin/stty",
		[]string{"stty", "echo"},
		&attrs,
	)
	if err != nil {
		return err
	}
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		return err
	}
	fmt.Println("")

	auth := smtp.PlainAuth("", config.MyEmail, pass, config.SmtpHost)
	tlsconfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         config.SmtpHost,
	}
	c, err := tls.Dial("tcp", config.SmtpHost+":465", tlsconfig)
	if err != nil {
		return err
	}
	conn, err := smtp.NewClient(c, config.SmtpHost)
	if err != nil {
		return err
	}
	defer conn.Quit()

	if err := conn.Auth(auth); err != nil {
		return err
	}
	if err := conn.Mail(config.MyEmail); err != nil {
		return err
	}
	if err := conn.Rcpt(to); err != nil {
		return err
	}
	wc, err := conn.Data()
	if err != nil {
		return err
	}
	defer wc.Close()

	_, err = wc.Write([]byte(report))
	return err
}

func main() {
	days := flag.Int("days", 0, "number days from now")
	toMe := flag.Bool("me", false, "send to me or to target")
	forceDate := flag.String("forceDate", "", "force date instead of 'days'")
	dryRun := flag.Bool("dryRun", false, "dry run")
	flag.Parse()

	var config Config
	if _, err := toml.DecodeFile("config.toml", &config); err != nil {
		panic(err)
	}

	var to string
	if *toMe {
		to = config.MyEmail
	} else {
		to = config.WorkEmail
	}

	var date string
	if *forceDate == "" {
		date = time.Now().AddDate(0, 0, *days).Format("02.01.2006")
	} else {
		date = *forceDate
	}

	if err := getAndDumpJIRATasks(&config); err != nil {
		panic(err)
	}

	cmdEdit := exec.Command("vim", config.InputFile)
	cmdEdit.Stdin = os.Stdin
	cmdEdit.Stdout = os.Stdout
	cmdEdit.Stderr = os.Stderr
	if err := cmdEdit.Run(); err != nil {
		panic(err)
	}
	fmt.Println("")

	descr, err := ioutil.ReadFile(config.InputFile)
	if err != nil {
		panic(err)
	}

	report := generateReport(&config, string(descr), to, date)

	if *dryRun {
		tempFile, err := ioutil.TempFile(os.TempDir(), "*")
		if err != nil {
			panic(err)
		}

		tempFile.WriteString("<html>" + report + "</html>")

		fn := tempFile.Name()
		if err = tempFile.Close(); err != nil {
			panic(err)
		}
		cmdOpen := exec.Command("firefox", fn)
		if err = cmdOpen.Run(); err != nil {
			panic(err)
		}

		return
	}

	if err := sendReport(&config, report, to); err != nil {
		panic(err)
	}
}
