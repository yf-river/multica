// Package health 保存实测数据、报告证据及可恢复的有效数据集合。
package health

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Profile struct {
	Name      string `json:"name"`
	Sex       string `json:"sex"`
	BirthDate string `json:"birth_date"`
}

type State struct {
	Profile               Profile  `json:"profile"`
	ObservationIDs        []string `json:"observation_ids"`
	CurrentObservationIDs []string `json:"current_observation_ids"`
}

type Observation struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	OriginalName       string `json:"original_name"`
	Category           string `json:"category"`
	Value              string `json:"value"`
	Unit               string `json:"unit"`
	OriginalValue      string `json:"original_value"`
	OriginalUnit       string `json:"original_unit"`
	ReferenceRange     string `json:"reference_range"`
	Abnormal           string `json:"abnormal"`
	ObservedAt         string `json:"observed_at"`
	Method             string `json:"method"`
	Specimen           string `json:"specimen"`
	Site               string `json:"site"`
	Source             string `json:"source"`
	ReportID           string `json:"report_id"`
	Page               int    `json:"page"`
	Evidence           string `json:"evidence"`
	Status             string `json:"status"`
	Uncertainty        string `json:"uncertainty"`
	Supersedes         string `json:"supersedes"`
	MeasurementGroupID string `json:"measurement_group_id,omitempty"`
}

type PageResult struct {
	Page   int    `json:"page"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

type Result struct {
	ContextRevision int64         `json:"context_revision"`
	ExamDate        string        `json:"exam_date"`
	Institution     string        `json:"institution"`
	PatientName     string        `json:"patient_name"`
	Observations    []Observation `json:"observations"`
	Pages           []PageResult  `json:"pages"`
	Analysis        string        `json:"analysis"`
	Conclusions     string        `json:"conclusions"`
	Uncertainty     string        `json:"uncertainty"`
}

var number = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)
var date = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func ValidateDate(value string) error {
	if !date.MatchString(value) {
		return fmt.Errorf("测量日期须为 YYYY-MM-DD")
	}
	t, err := time.Parse("2006-01-02", value)
	today, _ := time.Parse("2006-01-02", time.Now().Format("2006-01-02"))
	if err != nil || t.After(today) {
		return fmt.Errorf("测量日期无效或晚于今天")
	}
	return nil
}

func ValidateProfile(p Profile) error {
	if len([]rune(p.Name)) > 100 {
		return fmt.Errorf("姓名过长")
	}
	if p.Sex != "" && p.Sex != "男" && p.Sex != "女" && p.Sex != "其他" {
		return fmt.Errorf("性别值无效")
	}
	if p.BirthDate != "" {
		return ValidateDate(p.BirthDate)
	}
	return nil
}

// Normalize 保留原始值，只对已经确定的身高、体重单位进行换算。
func Normalize(o *Observation) error {
	o.Name = strings.TrimSpace(o.Name)
	o.Value = strings.TrimSpace(o.Value)
	o.Unit = strings.TrimSpace(o.Unit)
	if o.Name == "" || o.Value == "" || len([]rune(o.Value)) > 4000 {
		return fmt.Errorf("指标名称和结果必填，结果最多 4000 字")
	}
	if o.OriginalName == "" {
		o.OriginalName = o.Name
	}
	if o.OriginalValue == "" {
		o.OriginalValue = o.Value
	}
	if o.OriginalUnit == "" {
		o.OriginalUnit = o.Unit
	}
	aliases := map[string]string{"体重": "weight", "身高": "height", "BMI": "bmi", "体重指数": "bmi", "体质指数": "bmi", "收缩压": "systolic_pressure", "舒张压": "diastolic_pressure", "腰围": "waist", "体脂率": "body_fat", "心率": "resting_heart_rate", "脉搏": "resting_heart_rate", "静息心率": "resting_heart_rate"}
	if code, ok := aliases[o.Name]; ok {
		o.Code = code
	} else if o.Code == "" {
		o.Code = o.Name
	}
	if o.Source == "manual" && (o.Code == "weight" || o.Code == "height" || o.Code == "resting_heart_rate" || o.Code == "systolic_pressure" || o.Code == "diastolic_pressure") {
		v, err := strconv.ParseFloat(o.Value, 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("日常指标结果须为大于零的有效数值")
		}
	}
	if o.Category == "" {
		o.Category = "其他"
	}
	if o.Status == "" {
		o.Status = "accepted"
	}
	if o.Status != "accepted" && o.Status != "pending" && o.Status != "rejected" {
		return fmt.Errorf("指标确认状态无效")
	}
	if err := ValidateDate(o.ObservedAt); err != nil {
		if o.Source == "manual" {
			return err
		}
		o.Uncertainty = strings.TrimSpace(o.Uncertainty + " 原报告测量日期缺失或格式异常")
	}
	if number.MatchString(o.Value) {
		v, err := strconv.ParseFloat(o.Value, 64)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return fmt.Errorf("指标数值无效")
		}
		switch o.Code {
		case "height":
			if o.Unit == "m" || o.Unit == "米" {
				o.Value = strconv.FormatFloat(v*100, 'f', -1, 64)
				o.Unit = "cm"
			}
			if o.Unit == "厘米" {
				o.Unit = "cm"
			}
		case "weight":
			if o.Unit == "g" || o.Unit == "克" {
				o.Value = strconv.FormatFloat(v/1000, 'f', -1, 64)
				o.Unit = "kg"
			}
			if o.Unit == "千克" || o.Unit == "公斤" {
				o.Unit = "kg"
			}
		}
		if o.Unit == "" && o.Code != "bmi" {
			o.Uncertainty = strings.TrimSpace(o.Uncertainty + " 原报告未标注数值单位")
		}
	}
	return nil
}

// MetricKey 区分不同标本、方法、部位和不可比较的单位。
func MetricKey(o Observation) string {
	return strings.Join([]string{o.Code, o.Unit, o.Specimen, o.Method, o.Site}, "\x1f")
}

// CurrentKey 按检查项目、标本、方法和部位覆盖；单位由该次结果决定。
func CurrentKey(o Observation) string {
	return strings.Join([]string{o.Code, o.Specimen, o.Method, o.Site}, "\x1f")
}

func Selected(observations []Observation, ids []string) []Observation {
	byID := map[string]Observation{}
	for _, o := range observations {
		byID[o.ID] = o
	}
	result := []Observation{}
	for _, id := range ids {
		if o, ok := byID[id]; ok {
			result = append(result, o)
		}
	}
	return result
}

// Apply 保留历史记录，最后一批写入的同类结果成为当前值。
func Apply(state State, existing, incoming []Observation) State {
	next := State{Profile: state.Profile, ObservationIDs: append([]string{}, state.ObservationIDs...), CurrentObservationIDs: []string{}}
	byID := map[string]Observation{}
	for _, o := range existing {
		byID[o.ID] = o
	}
	keys, replaced := map[string]bool{}, map[string]bool{}
	for _, o := range incoming {
		keys[CurrentKey(o)] = true
		replaced[o.ID] = true
		if o.Supersedes != "" {
			replaced[o.Supersedes] = true
		}
	}
	ids := []string{}
	for _, id := range next.ObservationIDs {
		if !replaced[id] {
			ids = append(ids, id)
		}
	}
	next.ObservationIDs = ids
	last := map[string]int{}
	for i, o := range incoming {
		last[CurrentKey(o)] = i
		next.ObservationIDs = append(next.ObservationIDs, o.ID)
	}
	for i, o := range incoming {
		if last[CurrentKey(o)] == i {
			next.CurrentObservationIDs = append(next.CurrentObservationIDs, o.ID)
		}
	}
	for _, id := range state.CurrentObservationIDs {
		if o, ok := byID[id]; ok && !keys[CurrentKey(o)] && !replaced[id] {
			next.CurrentObservationIDs = append(next.CurrentObservationIDs, id)
		}
	}
	return next
}

// 历史快照没有保存采用集合时，按其原有日期和确认状态还原。
func ResolveCurrent(state *State, observations []Observation) {
	if state.CurrentObservationIDs != nil {
		return
	}
	state.CurrentObservationIDs = []string{}
	for _, o := range Current(observations) {
		state.CurrentObservationIDs = append(state.CurrentObservationIDs, o.ID)
	}
}

func Current(observations []Observation) []Observation {
	groups := map[string][]Observation{}
	for _, o := range observations {
		if o.Status != "accepted" {
			continue
		}
		k := MetricKey(o)
		v := groups[k]
		if len(v) == 0 || o.ObservedAt > v[0].ObservedAt {
			groups[k] = []Observation{o}
		} else if o.ObservedAt == v[0].ObservedAt {
			groups[k] = append(v, o)
		}
	}
	result := []Observation{}
	for _, rows := range groups {
		seen := map[string]bool{}
		for _, o := range rows {
			key := o.Value
			if !seen[key] {
				result = append(result, o)
				seen[key] = true
			}
		}
	}
	return result
}

func Age(birthDate string, today time.Time) *int {
	b, err := time.Parse("2006-01-02", birthDate)
	if err != nil {
		return nil
	}
	n := today.Year() - b.Year()
	if today.Month() < b.Month() || (today.Month() == b.Month() && today.Day() < b.Day()) {
		n--
	}
	return &n
}
