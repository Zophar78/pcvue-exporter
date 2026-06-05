package pcvue

import (
	"net/url"
	"strconv"
)

type VariableProperty int

const (
	VariablePropertyVariableName  VariableProperty = 0
	VariablePropertyDescription   VariableProperty = 1
	VariablePropertyTimestampType VariableProperty = 2
	VariablePropertyAssocLabel    VariableProperty = 3

	VariablePropertyTextAttr01 VariableProperty = 4
	VariablePropertyTextAttr02 VariableProperty = 5
	VariablePropertyTextAttr03 VariableProperty = 6
	VariablePropertyTextAttr04 VariableProperty = 7
	VariablePropertyTextAttr05 VariableProperty = 8
	VariablePropertyTextAttr06 VariableProperty = 9
	VariablePropertyTextAttr07 VariableProperty = 10
	VariablePropertyTextAttr08 VariableProperty = 11
	VariablePropertyTextAttr09 VariableProperty = 12
	VariablePropertyTextAttr10 VariableProperty = 13
	VariablePropertyTextAttr11 VariableProperty = 14
	VariablePropertyTextAttr12 VariableProperty = 15
	VariablePropertyTextAttr13 VariableProperty = 16
	VariablePropertyTextAttr14 VariableProperty = 17
	VariablePropertyTextAttr15 VariableProperty = 18
	VariablePropertyTextAttr16 VariableProperty = 19

	VariablePropertyDeferredTextAttr03 VariableProperty = 20
	VariablePropertyDeferredTextAttr04 VariableProperty = 21
	VariablePropertyDeferredTextAttr05 VariableProperty = 22
	VariablePropertyDeferredTextAttr06 VariableProperty = 23
	VariablePropertyDeferredTextAttr07 VariableProperty = 24
	VariablePropertyDeferredTextAttr08 VariableProperty = 25
	VariablePropertyDeferredTextAttr09 VariableProperty = 26
	VariablePropertyDeferredTextAttr10 VariableProperty = 27
	VariablePropertyDeferredTextAttr11 VariableProperty = 28
	VariablePropertyDeferredTextAttr12 VariableProperty = 29
	VariablePropertyDeferredTextAttr13 VariableProperty = 30
	VariablePropertyDeferredTextAttr14 VariableProperty = 31
	VariablePropertyDeferredTextAttr15 VariableProperty = 32
	VariablePropertyDeferredTextAttr16 VariableProperty = 33

	VariablePropertyBinAttr                VariableProperty = 34
	VariablePropertyMinValue               VariableProperty = 35
	VariablePropertyMaxValue               VariableProperty = 36
	VariablePropertyMinControlValue        VariableProperty = 37
	VariablePropertyMaxControlValue        VariableProperty = 38
	VariablePropertyMinVariableName        VariableProperty = 39
	VariablePropertyMaxVariableName        VariableProperty = 40
	VariablePropertyMinControlVariableName VariableProperty = 41
	VariablePropertyMaxControlVariableName VariableProperty = 42
	VariablePropertyUnit                   VariableProperty = 43
	VariablePropertyFormat                 VariableProperty = 44
	VariablePropertyAnim0AssocLabel        VariableProperty = 45
	VariablePropertyAnim1AssocLabel        VariableProperty = 46
	VariablePropertyCmd0AssocLabel         VariableProperty = 47
	VariablePropertyCmd1AssocLabel         VariableProperty = 48
	VariablePropertyLog0AssocLabel         VariableProperty = 49
	VariablePropertyLog1AssocLabel         VariableProperty = 50
	VariablePropertyFormattedValue         VariableProperty = 51
	VariablePropertyAlarmLevel             VariableProperty = 52
	VariablePropertyMaintenance            VariableProperty = 53
	VariablePropertyVariableType           VariableProperty = 54
)

func (p VariableProperty) String() string {
	return strconv.Itoa(int(p))
}

type AlarmProperty int

const (
	AlarmPropertyVariableName    AlarmProperty = 0
	AlarmPropertyAssocLabel      AlarmProperty = 1
	AlarmPropertyStandardLabel   AlarmProperty = 2
	AlarmPropertyUserName        AlarmProperty = 3
	AlarmPropertyStation         AlarmProperty = 4
	AlarmPropertyAlarmLevel      AlarmProperty = 5
	AlarmPropertyInformations    AlarmProperty = 6
	AlarmPropertyLogInformations AlarmProperty = 7
	AlarmPropertyTimestampType   AlarmProperty = 8
	AlarmPropertyDescription     AlarmProperty = 9

	AlarmPropertyTextAttr01 AlarmProperty = 10
	AlarmPropertyTextAttr02 AlarmProperty = 11
	AlarmPropertyTextAttr03 AlarmProperty = 12
	AlarmPropertyTextAttr04 AlarmProperty = 13
	AlarmPropertyTextAttr05 AlarmProperty = 14
	AlarmPropertyTextAttr06 AlarmProperty = 15
	AlarmPropertyTextAttr07 AlarmProperty = 16
	AlarmPropertyTextAttr08 AlarmProperty = 17
	AlarmPropertyTextAttr09 AlarmProperty = 18
	AlarmPropertyTextAttr10 AlarmProperty = 19
	AlarmPropertyTextAttr11 AlarmProperty = 20
	AlarmPropertyTextAttr12 AlarmProperty = 21
	AlarmPropertyTextAttr13 AlarmProperty = 22
	AlarmPropertyTextAttr14 AlarmProperty = 23
	AlarmPropertyTextAttr15 AlarmProperty = 24
	AlarmPropertyTextAttr16 AlarmProperty = 25

	AlarmPropertyDeferredTextAttr03 AlarmProperty = 26
	AlarmPropertyDeferredTextAttr04 AlarmProperty = 27
	AlarmPropertyDeferredTextAttr05 AlarmProperty = 28
	AlarmPropertyDeferredTextAttr06 AlarmProperty = 29
	AlarmPropertyDeferredTextAttr07 AlarmProperty = 30
	AlarmPropertyDeferredTextAttr08 AlarmProperty = 31
	AlarmPropertyDeferredTextAttr09 AlarmProperty = 32
	AlarmPropertyDeferredTextAttr10 AlarmProperty = 33
	AlarmPropertyDeferredTextAttr11 AlarmProperty = 34
	AlarmPropertyDeferredTextAttr12 AlarmProperty = 35
	AlarmPropertyDeferredTextAttr13 AlarmProperty = 36
	AlarmPropertyDeferredTextAttr14 AlarmProperty = 37
	AlarmPropertyDeferredTextAttr15 AlarmProperty = 38
	AlarmPropertyDeferredTextAttr16 AlarmProperty = 39

	AlarmPropertyBinAttr AlarmProperty = 40
)

func (p AlarmProperty) String() string {
	return strconv.Itoa(int(p))
}

func variablePropertiesToStrings(properties ...VariableProperty) []string {
	result := make([]string, 0, len(properties))

	for _, property := range properties {
		result = append(result, property.String())
	}

	return result
}

func addAlarmProperties(query url.Values, properties ...AlarmProperty) {
	for _, property := range properties {
		query.Add("Properties", property.String())
	}
}
