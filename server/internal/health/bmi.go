package health

import (
	"math"
	"strconv"
)

// BMI 是当前身高和体重的计算结果，来源记录独立保存。
type BMI struct {
	Value  float64     `json:"value"`
	Unit   string      `json:"unit"`
	Height Observation `json:"height"`
	Weight Observation `json:"weight"`
}

func CalculateBMI(current []Observation) *BMI {
	var height, weight *Observation
	for i := range current {
		o := &current[i]
		if o.Code == "height" && height == nil {
			height = o
		}
		if o.Code == "weight" && weight == nil {
			weight = o
		}
	}
	if height == nil || weight == nil {
		return nil
	}
	h, errH := strconv.ParseFloat(height.Value, 64)
	w, errW := strconv.ParseFloat(weight.Value, 64)
	if errH != nil || errW != nil || h <= 0 || w <= 0 || math.IsNaN(h) || math.IsNaN(w) || math.IsInf(h, 0) || math.IsInf(w, 0) {
		return nil
	}
	switch height.Unit {
	case "cm", "厘米":
		h /= 100
	case "m", "米":
	default:
		return nil
	}
	switch weight.Unit {
	case "kg", "公斤", "千克":
	case "g", "克":
		w /= 1000
	default:
		return nil
	}
	value := math.Round(w/(h*h)*10) / 10
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return nil
	}
	return &BMI{Value: value, Unit: "kg/m²", Height: *height, Weight: *weight}
}
