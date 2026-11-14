package common

import (
	"regexp"
	"strings"
)

var (
	usnRegex       = regexp.MustCompile(`^1DS2[1-6](AI|AE|AU|BT|CG|MD|ET|EC|ME|EE|CH|CB|IC|CD|CS|CV|IS|RI|EI|CY)[0-9]{3}$`)
	appNumberRegex = regexp.MustCompile(`^26UGDS[0-9]{4}$`)
	phoneRegex     = regexp.MustCompile(`^[0-9]{10}$`)
)

var branchCodeAliases = map[string]string{
	"AI": "AI", "AE": "AE", "AU": "AU", "BT": "BT", "CG": "CG",
	"MD": "MD", "ET": "ET", "EC": "EC", "ME": "ME", "EE": "EE",
	"CH": "CH", "CB": "CB", "IC": "IC", "CD": "CD", "CS": "CS",
	"CV": "CV", "IS": "IS", "RI": "RI", "EI": "EI", "CY": "CY",
	"ARTIFICIAL INTELLIGENCE AND MACHINE LEARNING": "AI",
	"AERONAUTICAL ENGINEERING":                     "AE",
	"AUTOMOBILE ENGINEERING":                        "AU",
	"BIOTECHNOLOGY":                                 "BT",
	"COMPUTER SCIENCE AND DESIGN":                   "CG",
	"MEDICAL ELECTRONICS ENGINEERING":               "MD",
	"ELECTRONICS AND TELECOMMUNICATION ENGINEERING": "ET",
	"ELECTRONICS AND COMMUNICATION ENGINEERING":     "EC",
	"ELECTRONICS AND INSTRUMENTATION ENGINEERING":   "EI",
	"MECHANICAL ENGINEERING":                        "ME",
	"ELECTRICAL AND ELECTRONICS ENGINEERING":        "EE",
	"CHEMICAL ENGINEERING":                          "CH",
	"COMPUTER SCIENCE AND ENGINEERING (CYBER SECURITY)": "CY",
	"COMPUTER SCIENCE AND ENGINEERING (DATA SCIENCE)":   "CD",
	"COMPUTER SCIENCE AND ENGINEERING (IOT AND CYBER SECURITY INCLUDING BLOCKCHAIN)": "IC",
	"INFORMATION SCIENCE AND ENGINEERING":           "IS",
	"COMPUTER SCIENCE AND ENGINEERING":              "CS",
	"COMPUTER SCIENCE AND BUSINESS SYSTEMS":         "CB",
	"CIVIL ENGINEERING":                             "CV",
	"ROBOTICS AND ARTIFICIAL INTELLIGENCE":          "RI",
}

// ResolveBranchCode normalizes department names and maps them to their 2-letter branch code.
func ResolveBranchCode(dept string) (string, bool) {
	clean := strings.ToUpper(strings.ReplaceAll(dept, "&", " AND "))
	clean = strings.Join(strings.Fields(clean), " ")
	code, ok := branchCodeAliases[clean]
	return code, ok
}

// ValidateUserInput validates the USN/Application Number, phone number, and department consistency.
func ValidateUserInput(usn string, mobile string, currentYear int, department string) (error, string) {
	usnUpper := strings.ToUpper(strings.TrimSpace(usn))
	mobileClean := strings.TrimSpace(mobile)

	if currentYear == 1 {
		if !appNumberRegex.MatchString(usnUpper) {
			return InvalidApplicationNumberError, "id"
		}
	} else {
		if !usnRegex.MatchString(usnUpper) {
			return InvalidUSNError, "id"
		}
		if department != "" {
			expectedCode, ok := ResolveBranchCode(department)
			if !ok || usnUpper[5:7] != expectedCode {
				return InvalidUSNBranchError, "id"
			}
		}
	}

	if !phoneRegex.MatchString(mobileClean) {
		return InvalidMobileNumberError, "mobile"
	}
	return nil, ""
}
