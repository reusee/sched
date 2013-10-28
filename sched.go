package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/howeyc/fsnotify"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var signals = make(chan os.Signal)
var watcher *fsnotify.Watcher

func init() {
	signal.Notify(signals, syscall.SIGUSR1)
	var err error
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	user, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	jobDir := filepath.Join(user.HomeDir, ".sched")
	_, err = os.Stat(jobDir)
	if os.IsNotExist(err) {
		err = os.Mkdir(jobDir, os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	} else if err != nil {
		log.Fatal(err)
	}
	err = watcher.Watch(jobDir)
	if err != nil {
		log.Fatal(err)
	}
	for {
		hasJob := checkJobs(jobDir)
		if hasJob {
			continue
		} else {
			select {
			case <-signals:
			case <-watcher.Event:
			}
			continue
		}
	}
}

const (
	EXPIRED = iota
	NOW
	WAIT
)

type Plan struct {
	Time    time.Time
	Comment string
	State   int
}

type Job struct {
	Cmd   string
	Args  []string
	Path  string
	Plans []*Plan
}

func (self *Job) Run() {
	fmt.Printf("Run: %s %v\n", self.Cmd, self.Args)
	exec.Command(self.Cmd, self.Args...).Start()
}

func checkJobs(jobDir string) (hasJob bool) {
	nextPlan := &Plan{Time: time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)}
	var nextJob *Job
	nowJobs := make([]*Job, 0)
	filepath.Walk(jobDir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		job := &Job{
			Path:  path,
			Plans: make([]*Plan, 0),
		}
		err = job.Parse()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return nil
		}
		for _, p := range job.Plans {
			switch p.State {
			case WAIT:
				if p.Time.Before(nextPlan.Time) {
					nextJob = job
					nextPlan = p
				}
			case NOW:
				nowJobs = append(nowJobs, job)
			case EXPIRED:
				fmt.Printf("Expired: %s %s %v %s\n", p.Time.Format(time.RFC822), job.Cmd, job.Args, p.Comment)
			}
		}
		return nil
	})

	for _, job := range nowJobs {
		job.Run()
	}

	if nextJob != nil {
		fmt.Printf("Next: %s -> %v -> %s\n", nextPlan.Time.Format(time.RFC822), nextPlan.Time.Sub(time.Now()), nextPlan.Comment)
		select {
		case <-time.After(nextPlan.Time.Sub(time.Now())):
			nextJob.Run()
			return true
		case <-signals:
			return true
		case <-watcher.Event:
			return true
		}
	}
	return false
}

const (
	parsingPlan = iota
	parsingArgs
)

func (self *Job) Parse() error {
	f, err := os.Open(self.Path)
	if err != nil {
		return err
	}
	input := bufio.NewReader(f)

	lines := make([]string, 0)
	for {
		line, err := input.ReadString('\n')
		line = strings.TrimSpace(line)
		if err == io.EOF {
			if line != "" && !strings.HasPrefix(line, "#") {
				lines = append(lines, line)
			}
			break
		} else if err != nil {
			return errors.New("reading file")
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}

	state := parsingPlan
	for i, line := range lines {
		switch state {
		case parsingPlan:
			if i == 0 || strings.HasPrefix(line, "and ") {
				line = strings.TrimPrefix(line, "and ")
				p, err := self.parsePlan(line)
				if err != nil {
					return errors.New("parse datetime")
				}
				self.Plans = append(self.Plans, p)
			} else {
				self.Cmd = line
				state = parsingArgs
			}
		case parsingArgs:
			self.Args = append(self.Args, line)
		}
	}
	return nil
}

func (self *Job) parsePlan(input string) (*Plan, error) {
	specs := make([]string, 0)
	inComment := false
	comments := make([]string, 0)
	for _, spec := range strings.Split(input, " ") {
		if inComment {
			comments = append(comments, spec)
		} else if strings.HasPrefix(spec, "#") {
			comments = append(comments, spec[1:])
			inComment = true
		} else {
			specs = append(specs, spec)
		}
	}
	comment := strings.Join(comments, " ")

	var year, month, day, hour, minute, second int
	var isRepeat, isHourRepeat, isDayRepeat, isWeekRepeat, isMonthRepeat bool
	var dayOfWeek time.Weekday
	var duration time.Duration

	datePattern := regexp.MustCompile(`^([0-9]{2})?[0-9]{2}-[0-9]{1,2}-[0-9]{1,2}|[0-9]{1,2}-[0-9]{1,2}$`)
	timePattern := regexp.MustCompile(`^[0-9]{1,2}:[0-9]{1,2}(:[0-9]{1,2})?$`)
	minuteSecondPattern := regexp.MustCompile(`^[0-9]{1,2}(:[0-9]{1,2})?$`)
	dayOfWeekPattern := regexp.MustCompile(`(?i)^sun[a-z]*|mon[a-z]*|tue[a-z]*|wed[a-z]*|thu[a-z]*|fri[a-z]*|sat[a-z]*$`)
	dayOfMonthPattern := regexp.MustCompile(`(?i)^[0-9]{1,2}(st|nd|rd|th)$`)
	durationPattern := regexp.MustCompile(`(?i)^~[0-9]+(h[a-z]*|m[a-z]*|s[a-z]*)$`)
	for _, spec := range specs {
		switch {
		case !isRepeat && datePattern.MatchString(spec): // date
			err := parseDate(spec, &year, &month, &day)
			if err != nil {
				return nil, err
			}
		case !isRepeat && timePattern.MatchString(spec): // time
			err := parseTime(spec, &hour, &minute, &second)
			if err != nil {
				return nil, err
			}
		case spec == "every": // repeat
			isRepeat = true
		case spec == "hour" && isRepeat: // hour repeat
			isHourRepeat = true
		case spec == "day" && isRepeat: // day repeat
			isDayRepeat = true
		case isRepeat && dayOfWeekPattern.MatchString(spec):
			err := parseDayOfWeek(spec, &dayOfWeek)
			if err != nil {
				return nil, err
			}
			isWeekRepeat = true
		case isWeekRepeat && timePattern.MatchString(spec):
			err := parseTime(spec, &hour, &minute, &second)
			if err != nil {
				return nil, err
			}
		case isRepeat && dayOfMonthPattern.MatchString(spec):
			err := parseDayOfMonth(spec, &day)
			if err != nil {
				return nil, err
			}
			isMonthRepeat = true
		case isMonthRepeat && timePattern.MatchString(spec):
			err := parseTime(spec, &hour, &minute, &second)
			if err != nil {
				return nil, err
			}
		case isHourRepeat && minuteSecondPattern.MatchString(spec):
			err := parseMinuteSecond(spec, &minute, &second)
			if err != nil {
				return nil, err
			}
		case isDayRepeat && timePattern.MatchString(spec):
			err := parseTime(spec, &hour, &minute, &second)
			if err != nil {
				return nil, err
			}
		case durationPattern.MatchString(spec):
			err := parseDuration(spec, &duration)
			if err != nil {
				return nil, err
			}
		default:
			fmt.Printf("Unknown time spec: %s\n", spec)
		}
	}

	var start time.Time
	var state int
	if !isRepeat {
		state = WAIT
		start = time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
		end := start.Add(duration)
		if time.Now().After(start) && time.Now().Before(end) {
			state = NOW
		} else if time.Now().After(end) {
			state = EXPIRED
		}
	} else if isHourRepeat {
		start, state = self.nextHourRepeat(duration, minute, second)
	} else if isDayRepeat {
		start, state = self.nextDayRepeat(duration, hour, minute, second)
	} else if isWeekRepeat {
		start, state = self.nextWeekRepeat(duration, dayOfWeek, hour, minute, second)
	} else if isMonthRepeat {
		start, state = self.nextMonthRepeat(duration, day, hour, minute, second)
	} else {
		return nil, errors.New("invalid time spec")
	}
	return &Plan{
		Time:    start,
		Comment: comment,
		State:   state,
	}, nil
}

func parseDate(spec string, year, month, day *int) error {
	dashCount := strings.Count(spec, "-")
	if dashCount == 2 {
		n, err := fmt.Sscanf(spec, "%d-%d-%d", year, month, day)
		if n != 3 || err != nil {
			return errors.New("parse date")
		}
		if *year < 1000 {
			*year += 2000
		}
	} else if dashCount == 1 {
		n, err := fmt.Sscanf(spec, "%d-%d", month, day)
		if n != 3 || err != nil {
			return errors.New("parse date")
		}
		*year = time.Now().Year()
	}
	return nil
}

func parseTime(spec string, hour, minute, second *int) error {
	semiCount := strings.Count(spec, ":")
	if semiCount == 2 {
		n, err := fmt.Sscanf(spec, "%d:%d:%d", hour, minute, second)
		if n != 3 || err != nil {
			return errors.New("parse time")
		}
	} else if semiCount == 1 {
		n, err := fmt.Sscanf(spec, "%d:%d", hour, minute)
		if n != 2 || err != nil {
			return errors.New("parse time")
		}
	}
	return nil
}

func parseMinuteSecond(spec string, minute, second *int) error {
	if strings.Contains(spec, ":") {
		n, err := fmt.Sscanf(spec, "%d:%d", minute, second)
		if n != 2 || err != nil {
			return errors.New("parse minute:second")
		}
	} else {
		n, err := fmt.Sscanf(spec, "%d", minute)
		if n != 1 || err != nil {
			return errors.New("parse minute")
		}
	}
	return nil
}

func parseDayOfWeek(spec string, day *time.Weekday) error {
	spec = strings.ToLower(spec)
	switch {
	case strings.HasPrefix(spec, "sun"):
		*day = time.Sunday
	case strings.HasPrefix(spec, "mon"):
		*day = time.Monday
	case strings.HasPrefix(spec, "tue"):
		*day = time.Tuesday
	case strings.HasPrefix(spec, "wed"):
		*day = time.Wednesday
	case strings.HasPrefix(spec, "thu"):
		*day = time.Thursday
	case strings.HasPrefix(spec, "fri"):
		*day = time.Friday
	case strings.HasPrefix(spec, "sat"):
		*day = time.Saturday
	default:
		return errors.New("parse day of week")
	}
	return nil
}

func parseDayOfMonth(spec string, day *int) error {
	ds := regexp.MustCompile(`[0-9]+`).FindString(spec)
	if ds == "" {
		return errors.New("parse day of month")
	}
	d, err := strconv.Atoi(ds)
	if err != nil {
		return errors.New("parse day of month")
	}
	*day = d
	return nil
}

func parseDuration(spec string, duration *time.Duration) error {
	groups := regexp.MustCompile(`(?i)^~([0-9]+)([a-z]+)$`).FindStringSubmatch(spec)
	n, err := strconv.Atoi(groups[1])
	if err != nil {
		return err
	}
	switch groups[2][0] {
	case 'h', 'H':
		*duration = time.Hour * time.Duration(n)
	case 'm', 'M':
		*duration = time.Minute * time.Duration(n)
	case 's', 'S':
		*duration = time.Second * time.Duration(n)
	}
	return nil
}

func (self *Job) nextHourRepeat(duration time.Duration, minute, second int) (time.Time, int) {
	if duration > time.Hour {
		duration = time.Hour
	}
	y, m, d := time.Now().Date()
	h, _, _ := time.Now().Clock()
	t := time.Date(y, m, d, h, minute, second, 0, time.Local)
	tEnd := t.Add(duration)
	if time.Now().After(t) && time.Now().Before(tEnd) {
		return t, NOW
	} else if time.Now().After(t) {
		t = t.Add(time.Hour * 1)
	}
	return t, WAIT
}

func (self *Job) nextDayRepeat(duration time.Duration, hour, minute, second int) (time.Time, int) {
	if duration > time.Hour*24 {
		duration = time.Hour * 24
	}
	y, m, d := time.Now().Date()
	t := time.Date(y, m, d, hour, minute, second, 0, time.Local)
	tEnd := t.Add(duration)
	if time.Now().After(t) && time.Now().Before(tEnd) {
		return t, NOW
	} else if time.Now().After(t) {
		t = t.Add(time.Hour * 24)
	}
	return t, WAIT
}

func (self *Job) nextWeekRepeat(duration time.Duration, dayOfWeek time.Weekday, hour, minute, second int) (time.Time, int) {
	if duration > time.Hour*24*7 {
		duration = time.Hour * 24 * 7
	}
	y, m, d := time.Now().Date()
	t := time.Date(y, m, d, hour, minute, second, 0, time.Local)
	for t.Weekday() != dayOfWeek {
		t = t.Add(time.Hour * 24)
	}
	tEnd := t.Add(duration)
	if time.Now().After(t) && time.Now().Before(tEnd) {
		return t, NOW
	} else if time.Now().After(t) {
		t = t.Add(time.Hour * 24)
		for t.Weekday() != dayOfWeek {
			t = t.Add(time.Hour * 24)
		}
	}
	return t, WAIT
}

func (self *Job) nextMonthRepeat(duration time.Duration, day, hour, minute, second int) (time.Time, int) {
	if duration > time.Hour*24*30 {
		duration = time.Hour * 24 * 30
	}
	y, m, _ := time.Now().Date()
	t := time.Date(y, m, day, hour, minute, second, 0, time.Local)
	for t.Day() != day {
		t = t.Add(time.Hour * 24)
	}
	tEnd := t.Add(duration)
	if time.Now().After(t) && time.Now().Before(tEnd) {
		return t, NOW
	} else if time.Now().After(t) {
		t = t.Add(time.Hour * 24)
		for t.Day() != day {
			t = t.Add(time.Hour * 24)
		}
	}
	return t, WAIT
}
