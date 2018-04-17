/*
* Copyright © 2017. TIBCO Software Inc.
* This file is subject to the license terms contained
* in the license file that is distributed with this file.
 */
package util

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TIBCOSoftware/flogo-lib/logger"
	"github.com/oliveagle/jsonpath"
	// "github.com/TIBCOSoftware/flogo-lib/logger"
	// "github.com/jeffreybozek/jsonpath"
	// "github.com/pkg/errors"
)

var newLogger = logger.GetLogger("jsonpath-eval")

// func JsonPathEval(jsonData string, expression string) (*string, error) {
// 	paths, err := jsonpath.ParsePaths(expression)
// 	if err != nil {
// 		return nil, err
// 	}
// 	newLogger.Debugf("jsonpath expression is [%v], data is [%v]", expression, jsonData)

// 	eval, err := jsonpath.EvalPathsInBytes([]byte(jsonData), paths)
// 	if err != nil {
// 		newLogger.Infof("unable to evaluate jsonpath expression, error is [%v]", err)
// 		return nil, err
// 	}

// 	for {
// 		if result, ok := eval.Next(); ok {
// 			//return after the first match
// 			value := string(result.Value) //The value obtained will be encased in double quote characters, e.g. "USA" when the value happens to be USA.
// 			//Trim the double quote suffix and prefix
// 			value = strings.TrimPrefix(value, "\"")
// 			value = strings.TrimSuffix(value, "\"")
// 			newLogger.Debugf("jsonpath [%v] evaluated to value [%v]", expression, value)
// 			return &value, nil // true -> show keys in pretty string
// 		} else {
// 			return nil, errors.New(fmt.Sprintf("Error evaluating jsonpath expression[%v] on content [%v]", expression, jsonData))
// 		}
// 	}
// }

func JsonPathEval(jsonData string, expression string) (*string, error) {
	fmt.Println("##########################################")
	var jsondata interface{}
	json.Unmarshal([]byte(jsonData), &jsondata)

	res, err := jsonpath.JsonPathLookup(jsondata, expression)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Error evaluating jsonpath expression[%v] on content [%v]", expression, jsonData))
	}
	strVar := res.(string)
	newLogger.Debugf("jsonpath [%v] evaluated to value [%v]", expression, strVar)
	fmt.Println("##########################################")
	return &strVar, nil

}
