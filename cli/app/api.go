/*
* Copyright © 2017. TIBCO Software Inc.
* This file is subject to the license terms contained
* in the license file that is distributed with this file.
 */
package app

import (
	"encoding/json"
	"io/ioutil"
	"path/filepath"
	"strings"

	"fmt"
	"os"
	"path"

	"bytes"

	"reflect"
	"strconv"

	api "github.com/TIBCOSoftware/flogo-cli/app"
	config "github.com/TIBCOSoftware/flogo-cli/config"
	"github.com/TIBCOSoftware/flogo-cli/dep"
	"github.com/TIBCOSoftware/flogo-cli/util"
	"github.com/TIBCOSoftware/flogo-lib/app"
	faction "github.com/TIBCOSoftware/flogo-lib/core/action"
	ftrigger "github.com/TIBCOSoftware/flogo-lib/core/trigger"
	assets "github.com/TIBCOSoftware/mashling/cli/assets"
	"github.com/TIBCOSoftware/mashling/cli/env"
	"github.com/spf13/viper"

	"github.com/TIBCOSoftware/mashling/lib/model"
	"github.com/TIBCOSoftware/mashling/lib/types"
	"github.com/TIBCOSoftware/mashling/lib/util"
	"github.com/xeipuuv/gojsonschema"
)

// CreateMashling creates a gateway application from the specified json gateway descriptor
func CreateMashling(gatewayJSON string, defaultAppFlag bool, appDir, appName, pingPort string) error {

	flogoJSON, gatewayJSON, appName, err := TranslateGatewayJSON2FlogoJSON(gatewayJSON, pingPort, appName)
	if err != nil {
		fmt.Fprint(os.Stderr, "Error: Error while processing gateway descriptor.\n\n")
		return err
	}

	err = CreateApp(SetupNewProjectEnv(), flogoJSON, defaultAppFlag, appDir, appName, gatewayJSON)
	if err != nil {
		return err
	}

	fmt.Println("Generated mashling Artifacts.")
	fmt.Println("Building mashling Artifacts.")

	embed := util.Flogo_App_Embed_Config_Property_Default

	envFlogoEmbed := os.Getenv(util.Flogo_App_Embed_Config_Property)
	if len(envFlogoEmbed) > 0 {
		embed, err = strconv.ParseBool(os.Getenv(util.Flogo_App_Embed_Config_Property))
	}

	envProj := SetupExistingProjectEnv(appDir)

	err = customizeMainFile(envProj.GetAppDir(), appName, gatewayJSON)

	if err != nil {
		return err
	}

	options := &api.BuildOptions{SkipPrepare: false, PrepareOptions: &api.PrepareOptions{OptimizeImports: false, EmbedConfig: embed}}
	api.BuildApp(envProj, options)
	//delete flogo.json file from the app dir
	fgutil.DeleteFilesWithPrefix(appDir, "flogo")

	fmt.Println("Mashling gateway successfully built!")

	return nil
}

// TranslateGatewayJSON2FlogoJSON tanslates mashling json to flogo json
func TranslateGatewayJSON2FlogoJSON(gatewayJSON string, pingPort, appName string) (string, string, string, error) {
	descriptor, err := model.ParseGatewayDescriptor(gatewayJSON)
	if err != nil {
		return "", "", "", err
	}

	descriptor, err = appendPingDescriptor(pingPort, descriptor)
	if err != nil {
		return "", "", "", err
	}

	if appName != "" {
		altJSON := strings.Replace(gatewayJSON, `"`+descriptor.Gateway.Name+`"`, `"`+appName+`"`, 1)
		altDescriptor, err := model.ParseGatewayDescriptor(altJSON)

		//see if we can get away with simple replace so we don't reorder the existing json
		if err == nil && altDescriptor.Gateway.Name == appName {
			gatewayJSON = altJSON
		} else {
			//simple replace didn't work so we have to unmarshal & re-marshal the supplied json
			var appObj map[string]interface{}
			err := json.Unmarshal([]byte(gatewayJSON), &appObj)
			if err != nil {
				return "", "", "", err
			}

			appObj["name"] = appName

			updApp, err := json.MarshalIndent(appObj, "", "  ")
			if err != nil {
				return "", "", "", err
			}
			gatewayJSON = string(updApp)
		}

		descriptor.Gateway.Name = appName
	} else {
		appName = descriptor.Gateway.Name
	}

	flogoAppTriggers := []*ftrigger.Config{}
	flogoAppActions := []*faction.Config{}

	//1. load the configuration, if provided.
	configNamedMap := make(map[string]types.Config)
	for _, config := range descriptor.Gateway.Configurations {
		configNamedMap[config.Name] = config
	}

	triggerNamedMap := make(map[string]types.Trigger)
	for _, trigger := range descriptor.Gateway.Triggers {
		triggerNamedMap[trigger.Name] = trigger
	}

	handlerNamedMap := make(map[string]types.EventHandler)
	for _, evtHandler := range descriptor.Gateway.EventHandlers {
		handlerNamedMap[evtHandler.Name] = evtHandler
	}

	createdHandlers := make(map[string]bool)

	//new map to maintain existing trigger and its settings, to be used in comparing one trigger definition with another
	createdTriggersMap := make(map[string]*ftrigger.Config)

	//translate the gateway model to the flogo model
	for _, link := range descriptor.Gateway.EventLinks {
		triggerNames := link.Triggers

		for _, triggerName := range triggerNames {
			dispatches := link.Dispatches

			//create trigger sections for flogo
			/**
			TODO handle condition parsing and setting the condition in the trigger.
			//get the condition if available
			condition := path.If
			//create the trigger using the condition
			.......
			*/
			flogoTrigger, isNew, err := model.CreateFlogoTrigger(configNamedMap, triggerNamedMap[triggerName], handlerNamedMap, dispatches, createdTriggersMap)
			if err != nil {
				return "", "", "", err
			}

			//	check if the trigger is a new trigger or a "logically" same trigger.
			if *isNew {
				//looks like a new trigger has been added
				flogoAppTriggers = append(flogoAppTriggers, flogoTrigger)
			} else {
				//looks like an existing trigger with matching settings is found and is now modified with a new handler
				for index, v := range flogoAppTriggers {
					if v.Name == flogoTrigger.Name {
						// Found the old trigger entry in the list!
						//remove it..
						flogoAppTriggers = append(flogoAppTriggers[:index], flogoAppTriggers[index+1:]...)
						//...and add the modified trigger to the list
						flogoAppTriggers = append(flogoAppTriggers, flogoTrigger)
					}
				}
			}

			//create unique handler actions
			for _, dispatch := range dispatches {
				handlerName := dispatch.Handler

				if !createdHandlers[handlerName] {
					//not already created, so create it
					flogoAction, err := model.CreateFlogoFlowAction(handlerNamedMap[handlerName])
					if err != nil {
						return "", "", "", err
					}

					flogoAppActions = append(flogoAppActions, flogoAction)
					createdHandlers[handlerName] = true
				}
			}
		}

	}

	flogoApp := app.Config{
		Name:        descriptor.Gateway.Name,
		Type:        util.Flogo_App_Type,
		Version:     descriptor.Gateway.Version,
		Description: descriptor.Gateway.Description,
		Triggers:    flogoAppTriggers,
		Actions:     flogoAppActions,
	}

	//create flogo PP JSON
	bytes, err := json.MarshalIndent(flogoApp, "", "\t")
	if err != nil {
		return "", "", "", nil
	}

	flogoJSON := string(bytes)

	return flogoJSON, gatewayJSON, appName, nil
}

// BuildMashling Builds mashling gateway
func BuildMashling(appDir string, gatewayJSON string, pingPort string) error {

	//create flogo.json from gateway descriptor
	flogoJSON, gatewayJSON, _, err := TranslateGatewayJSON2FlogoJSON(gatewayJSON, pingPort, "")
	if err != nil {
		fmt.Fprint(os.Stderr, "Error: Error while processing gateway descriptor.\n\n")
		return err
	}
	err = fgutil.CreateFileFromString(path.Join(appDir, "flogo.json"), flogoJSON)
	if err != nil {
		fmt.Fprint(os.Stderr, "Error: Error while creating flogo.json.\n\n")
		return err
	}

	embed := util.Flogo_App_Embed_Config_Property_Default

	envFlogoEmbed := os.Getenv(util.Flogo_App_Embed_Config_Property)
	if len(envFlogoEmbed) > 0 {
		embed, err = strconv.ParseBool(os.Getenv(util.Flogo_App_Embed_Config_Property))
	}

	options := &api.BuildOptions{SkipPrepare: false, PrepareOptions: &api.PrepareOptions{OptimizeImports: false, EmbedConfig: embed}}
	api.BuildApp(SetupExistingProjectEnv(appDir), options)

	//delete flogo.json file from the app dir
	fgutil.DeleteFilesWithPrefix(appDir, "flogo")

	fmt.Println("Mashling gateway successfully built!")

	return nil

}

// PublishToMashery publishes to mashery
func PublishToMashery(user *ApiUser, appDir string, gatewayJSON string, host string, mock bool, iodocs bool, testplan bool, apiTemplateJSON string) error {
	// Get HTTP triggers from JSON
	swaggerDoc, err := generateSwagger(host, "", gatewayJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to generate swagger doc\n\n")
		return err
	}

	token, err := user.FetchOAuthToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to fetch the OAauth token\n\n")
		return err
	}

	// Delay to avoid hitting QPS limit
	shortDelay()

	mApi, err := TransformSwagger(user, string(swaggerDoc), "swagger2", "masheryapi", token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to transform swagger to mashery api\n\n")
		return err
	}

	shortDelay()

	mIodoc, err := TransformSwagger(user, string(swaggerDoc), "swagger2", "iodocsv1", token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to transform swagger to mashery iodocs\n\n")
		return err
	}

	shortDelay()

	templApi, templEndpoint, templPackage, templPlan := BuildMasheryTemplates(apiTemplateJSON)
	if mock == false {

		mApi = UpdateApiWithDefaults(mApi, templApi, templEndpoint)

		apiId, apiName, endpoints, updated := CreateOrUpdateApi(user, token, MapToByteArray(mApi), mApi)

		if iodocs == true {

			cleanedTfIodocSwaggerDoc := UpdateIodocsDataWithApi(MapToByteArray(mIodoc), apiId)

			CreateOrUpdateIodocs(user, token, cleanedTfIodocSwaggerDoc, apiId, updated)
			shortDelay()
		}

		var key string
		if testplan == true {

			packagePlanDoc := CreatePackagePlanDataFromApi(apiId, apiName, endpoints)
			packagePlanDoc = UpdatePackageWithDefaults(packagePlanDoc, templPackage, templPlan)
			var marshalledDoc []byte
			marshalledDoc, err = json.Marshal(packagePlanDoc)
			if err != nil {
				panic(err)
			}

			shortDelay()

			p := CreateOrUpdatePackage(user, token, marshalledDoc, apiName, updated)

			shortDelay()

			key = CreateApplicationAndKey(user, token, p, apiName)

		}
		fmt.Println("==================================================================")
		fmt.Printf("Successfully published to mashery= API %s (id=%s)\n", apiName, apiId)
		fmt.Println("==================================================================")
		fmt.Println("API Control Center Link: https://" + strings.Replace(user.portal, "api", "admin", -1) + "/control-center/api-definitions/" + apiId)
		if testplan == true {
			fmt.Println("==================================================================")
			fmt.Println("Example Curls:")
			for _, endpoint := range endpoints {
				ep := endpoint.(map[string]interface{})
				fmt.Println(GenerateExampleCall(ep, key))
			}
		}
	} else {
		var prettyJSON bytes.Buffer
		err := json.Indent(&prettyJSON, MapToByteArray(mApi), "", "\t")
		if err != nil {
			return err
		}

		//fmt.Printf("%s", prettyJSON.Bytes())
		fmt.Println("\nMocked! Did not attempt to publish.\n")
	}

	return nil
}

func UpdateApiWithDefaults(mApi map[string]interface{}, templApi map[string]interface{}, templEndpoint map[string]interface{}) map[string]interface{} {
	var m1 map[string]interface{}
	json.Unmarshal(MapToByteArray(mApi), &m1)
	merged := merge(m1, templApi, 0)
	m_d := m1["endpoints"].([]interface{})

	items := []map[string]interface{}{}

	for _, d_item := range m_d {
		merged := merge(d_item.(map[string]interface{}), templEndpoint, 0)
		items = append(items, merged)
	}

	merged["endpoints"] = items
	return merged

}

func UpdatePackageWithDefaults(mApi map[string]interface{}, templPackage map[string]interface{}, templPlan map[string]interface{}) map[string]interface{} {
	var m1 map[string]interface{}
	json.Unmarshal(MapToByteArray(mApi), &m1)
	merged := merge(m1, templPackage, 0)
	m_d := m1["plans"].([]interface{})

	items := []map[string]interface{}{}

	for _, d_item := range m_d {
		merged := merge(d_item.(map[string]interface{}), templPlan, 0)
		items = append(items, merged)
	}

	merged["plans"] = items
	return merged

}

func BuildMasheryTemplates(apiTemplateJSON string) (map[string]interface{}, map[string]interface{}, map[string]interface{}, map[string]interface{}) {
	apiTemplate := map[string]interface{}{}
	endpointTemplate := map[string]interface{}{}
	packageTemplate := map[string]interface{}{}
	planTemplate := map[string]interface{}{}

	if apiTemplateJSON != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(apiTemplateJSON), &m); err != nil {
			panic(err)
		}
		apiTemplate = m["api"].(map[string]interface{})
		endpointTemplate = apiTemplate["endpoint"].(map[string]interface{})
		delete(apiTemplate, "endpoint")
		packageTemplate = m["package"].(map[string]interface{})
		planTemplate = packageTemplate["plan"].(map[string]interface{})
		delete(packageTemplate, "plan")

	} else {
		apiTemplate["qpsLimitOverall"] = 0
		endpointTemplate["requestAuthenticationType"] = "apiKeyAndSecret_SHA256"
		packageTemplate["sharedSecretLength"] = 10
		planTemplate["selfServiceKeyProvisioningEnabled"] = false

	}

	return apiTemplate, endpointTemplate, packageTemplate, planTemplate
}
func TransformSwagger(user *ApiUser, swaggerDoc string, sourceFormat string, targetFormat string, oauthToken string) (map[string]interface{}, error) {
	tfSwaggerDoc, err := user.TransformSwagger(string(swaggerDoc), sourceFormat, targetFormat, oauthToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to transform swagger doc\n\n")
	}

	// Only need the value of 'document'. Including the rest will cause errors
	var m map[string]interface{}
	if err = json.Unmarshal([]byte(tfSwaggerDoc), &m); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to process swagger doc\n\n")
	}

	return m, err
}

func MapToByteArray(mapToConvert map[string]interface{}) []byte {
	var convertedByteArray []byte
	var err error

	if val, ok := mapToConvert["document"]; ok {
		mapToConvert = val.(map[string]interface{})
	}

	if convertedByteArray, err = json.Marshal(mapToConvert); err != nil {
		panic(err)
	}

	return convertedByteArray
}

func CreateOrUpdateApi(user *ApiUser, token string, cleanedTfApiSwaggerDoc []byte, mApi map[string]interface{}) (string, string, []interface{}, bool) {
	updated := false

	masheryObject := "services"
	masheryObjectProperties := "id,name,endpoints.id,endpoints.name,endpoints.inboundSslRequired,endpoints.outboundRequestTargetPath,endpoints.outboundTransportProtocol,endpoints.publicDomains,endpoints.requestAuthenticationType,endpoints.requestPathAlias,endpoints.requestProtocol,endpoints.supportedHttpMethods,endoints.systemDomains,endpoints.trafficManagerDomain"
	var apiId string
	var apiName string
	var endpoints [](interface{})

	api, err := user.Read(masheryObject, "name:"+mApi["name"].(string), masheryObjectProperties, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to fetch api\n\n")
		panic(err)
	}

	shortDelay()

	var f [](interface{})
	if err = json.Unmarshal([]byte(api), &f); err != nil {
		panic(err)
	}
	if len(f) == 0 {
		s, err := user.Create(masheryObject, masheryObjectProperties, string(cleanedTfApiSwaggerDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create the api %s\n\n", s)
			fmt.Errorf("%v", err)
			panic(err)
		}
		apiId, apiName, endpoints = GetApiDetails(s)

	} else {
		m := f[0].(map[string]interface{})
		var m1 map[string]interface{}
		json.Unmarshal(cleanedTfApiSwaggerDoc, &m1)
		merged := merge(m, m1, 0)
		var mergedDoc []byte
		if mergedDoc, err = json.Marshal(merged); err != nil {
			panic(err)
		}
		serviceId := merged["id"].(string)
		s, err := user.Update(masheryObject+"/"+serviceId, masheryObjectProperties, string(mergedDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to update the api %s\n\n", s)
			fmt.Errorf("%v", err)
			panic(err)
		}
		apiId, apiName, endpoints = GetApiDetails(s)

		updated = true
	}

	return apiId, apiName, endpoints, updated
}

func merge(dst, src map[string]interface{}, depth int) map[string]interface{} {
	for key, srcVal := range src {
		if dstVal, ok := dst[key]; ok {
			if reflect.ValueOf(dstVal).Kind() == reflect.Map {
				srcMap, srcMapOk := mapify(srcVal)
				dstMap, dstMapOk := mapify(dstVal)
				if srcMapOk && dstMapOk {
					srcVal = merge(dstMap, srcMap, depth+1)
				}
			} else if (key == "endpoints" || key == "plans") && reflect.ValueOf(dstVal).Kind() == reflect.Slice {
				m_d := dstVal.([]interface{})
				m_s := srcVal.([]interface{})
				items := []map[string]interface{}{}

				for _, d_item := range m_d {
					i_d := d_item.(map[string]interface{})
					var i_s map[string]interface{}
					for _, s_item := range m_s {
						i_s = s_item.(map[string]interface{})
						if i_s["requestPathAlias"] == i_d["requestPathAlias"] {
							i_s2 := merge(i_d, i_s, depth+1)
							items = append(items, i_s2)
						}
					}
				}

				for _, s_item := range m_s {
					i_s := s_item.(map[string]interface{})
					if !MatchingEndpoint(i_s, m_d) {
						items = append(items, i_s)
					}
				}
				srcVal = items
			}
		}

		dst[key] = srcVal
	}
	return dst
}

func MatchingEndpoint(ep map[string]interface{}, epList []interface{}) bool {
	var i_d map[string]interface{}
	for _, d_item := range epList {
		i_d = d_item.(map[string]interface{})
		if i_d["requestPathAlias"] == ep["requestPathAlias"] {
			return true
		}
	}
	return false
}

func mapify(i interface{}) (map[string]interface{}, bool) {
	value := reflect.ValueOf(i)
	if value.Kind() == reflect.Map {
		m := map[string]interface{}{}
		for _, k := range value.MapKeys() {
			m[k.String()] = value.MapIndex(k).Interface()
		}
		return m, true
	}
	return map[string]interface{}{}, false
}

func CreateOrUpdateIodocs(user *ApiUser, token string, cleanedTfIodocSwaggerDoc []byte, apiId string, updated bool) {
	masheryObject := "iodocs/services"
	masheryObjectProperties := "id"

	item, err := user.Read(masheryObject, "serviceId:"+apiId, masheryObjectProperties, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to fetch iodocs\n\n")
		panic(err)
	}

	var f [](interface{})
	if err = json.Unmarshal([]byte(item), &f); err != nil {
		panic(err)
	}

	shortDelay()

	if len(f) == 0 {
		s, err := user.Create(masheryObject, masheryObjectProperties, string(cleanedTfIodocSwaggerDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create the iodocs %s\n\n", s)
			fmt.Errorf("%v", err)
		}
	} else {
		s, err := user.Update(masheryObject+"/"+apiId, masheryObjectProperties, string(cleanedTfIodocSwaggerDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create the iodocs %s\n\n", s)
			fmt.Errorf("%v", err)
		}
	}
}

func CreateOrUpdatePackage(user *ApiUser, token string, packagePlanDoc []byte, apiName string, updated bool) string {
	var p string
	masheryObject := "packages"
	masheryObjectProperties := "id,name,plans.id,plans.name"

	item, err := user.Read(masheryObject, "name:"+apiName, masheryObjectProperties, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to fetch package\n\n")
		panic(err)
	}

	var f [](interface{})
	if err = json.Unmarshal([]byte(item), &f); err != nil {
		panic(err)
	}

	if len(f) == 0 {
		p, err = user.Create(masheryObject, masheryObjectProperties, string(packagePlanDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create the package %s\n\n", p)
			fmt.Errorf("%v", err)
			panic(err)
		}
	} else {

		m := f[0].(map[string]interface{})

		var m1 map[string]interface{}
		json.Unmarshal(packagePlanDoc, &m1)
		merged := merge(m, m1, 0)
		var mergedDoc []byte
		if mergedDoc, err = json.Marshal(merged); err != nil {
			panic(err)
		}
		packageId := merged["id"].(string)
		p, err = user.Update(masheryObject+"/"+packageId, masheryObjectProperties, string(mergedDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to update the package %s\n\n", p)
			fmt.Errorf("%v", err)
			panic(err)
		}
	}
	return p
}

func GetApiDetails(api string) (string, string, []interface{}) {
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(api), &m); err != nil {
		panic(err)
	}
	return m["id"].(string), m["name"].(string), m["endpoints"].([]interface{}) // getting the api id and name
}

func GetPackagePlanDetails(packagePlan string) (string, string) {
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(packagePlan), &m); err != nil {
		panic(err)
	}
	plans := m["plans"].([]interface{})
	plan := plans[0].(map[string]interface{})
	return m["id"].(string), plan["id"].(string) // getting the package id and plan id
}

func UpdateIodocsDataWithApi(ioDoc []byte, apiId string) []byte {
	// need to create a different json representation for an IOdocs post body
	m1 := map[string]interface{}{}
	if err := json.Unmarshal([]byte(string(ioDoc)), &m1); err != nil {
		panic(err)
	}

	var cleanedTfIodocSwaggerDoc []byte

	m := map[string]interface{}{}
	m["definition"] = m1
	m["serviceId"] = apiId
	cleanedTfIodocSwaggerDoc, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}

	return cleanedTfIodocSwaggerDoc
}

func CreatePackagePlanDataFromApi(apiId string, apiName string, endpoints []interface{}) map[string]interface{} {
	pack := map[string]interface{}{}
	pack["name"] = apiName
	pack["sharedSecretLength"] = 10

	plan := map[string]interface{}{}
	plan["name"] = apiName
	plan["selfServiceKeyProvisioningEnabled"] = false
	plan["numKeysBeforeReview"] = 1

	service := map[string]interface{}{}
	service["id"] = apiId

	service["endpoints"] = endpoints

	planServices := []map[string]interface{}{}
	planServices = append(planServices, service)

	plan["services"] = planServices

	plans := []map[string]interface{}{}
	plans = append(plans, plan)
	pack["plans"] = plans

	return pack
}

func CreateApplicationAndKey(user *ApiUser, token string, packagePlan string, apiName string) string {
	var key string
	member, err := user.Read("members", "username:"+user.username, "id,username,applications,packageKeys", token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Unable to fetch api\n\n")
		panic(err)
	}

	var f [](interface{})
	if err = json.Unmarshal([]byte(member), &f); err != nil {
		panic(err)
	}

	var f_app interface{}
	testApplication := map[string]interface{}{}
	m := f[0].(map[string]interface{})
	var f2 [](interface{})
	f2 = m["applications"].([](interface{}))
	for _, application := range f2 {
		if application.(map[string]interface{})["name"] == "Test Application: "+apiName {
			testApplication = application.(map[string]interface{})
			packageKeys, err := user.Read("applications/"+testApplication["id"].(string)+"/packageKeys", "", "id,apikey,secret", token)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Unable to fetch packagekeys\n\n")
				panic(err)
			}

			var f [](interface{})
			if err = json.Unmarshal([]byte(packageKeys), &f); err != nil {
				panic(err)
			}
			if len(f) > 0 {
				pk := f[0].(map[string]interface{})

				testKeyDoc, err := json.Marshal(pk)
				if err != nil {
					panic(err)
				}
				key = string(testKeyDoc)
			}
			f_app = testApplication
		}
	}

	if len(testApplication) == 0 {
		testApplication["name"] = "Test Application: " + apiName
		testApplication["username"] = user.username
		testApplication["is_packaged"] = true
		var testApplicationDoc []byte

		testApplicationDoc, err = json.Marshal(testApplication)
		if err != nil {
			panic(err)
		}
		application, err := user.Create("members/"+m["id"].(string)+"/applications", "id,name", string(testApplicationDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create application\n\n")
			panic(err)
		}

		if err = json.Unmarshal([]byte(application), &f_app); err != nil {
			panic(err)
		}

	}

	if key == "" {
		packageId, planId := GetPackagePlanDetails(packagePlan)
		keyToCreate := map[string]interface{}{}
		keyPackage := map[string]interface{}{}
		keyPackage["id"] = packageId
		keyPlan := map[string]interface{}{}
		keyPlan["id"] = planId
		keyToCreate["package"] = keyPackage
		keyToCreate["plan"] = keyPlan
		var testKeyDoc []byte

		testKeyDoc, err = json.Marshal(keyToCreate)
		if err != nil {
			panic(err)
		}
		key, err = user.Create("applications/"+f_app.(map[string]interface{})["id"].(string)+"/packageKeys", "", string(testKeyDoc), token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to create key\n\n")
			panic(err)
		}
	}

	return key

}

func GenerateExampleCall(endpoint map[string]interface{}, key string) string {
	var exampleCall string

	public_domains := endpoint["publicDomains"].([]interface{})
	pd_map := public_domains[0].(map[string]interface{})
	var pk map[string]interface{}
	if err := json.Unmarshal([]byte(key), &pk); err != nil {
		panic(err)
	}
	protocol := "https"
	if !endpoint["inboundSslRequired"].(bool) {
		protocol = "http"
	}
	sig := ""
	if endpoint["requestAuthenticationType"] == "apiKeyAndSecret_SHA256" {
		sig = "&sig='$(php -r \"echo hash('sha256', '" + pk["apikey"].(string) + "'.'" + pk["secret"].(string) + "'.time());\")"
	}
	exampleCall = "curl -i -v -k -X " + strings.ToUpper(endpoint["supportedHttpMethods"].([]interface{})[0].(string)) + " '" + protocol + "://" + pd_map["address"].(string) + endpoint["requestPathAlias"].(string) + "?api_key=" + pk["apikey"].(string) + sig
	return exampleCall
}

// GetGatewayDetails returns gateway details i.e all Triggers, Handlers & Links
func GetGatewayDetails(env env.Project, cType ComponentType) (string, error) {
	gwInfoBuffer := bytes.NewBufferString("")
	rootDir := env.GetRootDir()
	mashlingDescriptorFile := rootDir + "/" + util.Gateway_Definition_File_Name
	mashlingJSON, err := fgutil.LoadLocalFile(mashlingDescriptorFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Error loading app file '%s' - %s\n\n", mashlingDescriptorFile, err.Error())
		os.Exit(2)
	}
	microgateway, err := model.ParseGatewayDescriptor(mashlingJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Error while parsing gateway description json - %s\n\n", err.Error())
		os.Exit(2)
	}

	tmpString := ""
	//Triggers info
	if cType == TRIGGER || cType == ALL {
		tmpString = fmt.Sprintf("Triggers: %d\n", len(microgateway.Gateway.Triggers))
		gwInfoBuffer.WriteString(tmpString)
		for _, trigger := range microgateway.Gateway.Triggers {
			tmpString = "\t" + trigger.Name + "  " + trigger.Type + "\n"
			gwInfoBuffer.WriteString(tmpString)
		}
	}

	//Handlers info
	if cType == HANDLER || cType == ALL {
		tmpString = fmt.Sprintf("Handlers: %d\n", len(microgateway.Gateway.EventHandlers))
		gwInfoBuffer.WriteString(tmpString)
		for _, handler := range microgateway.Gateway.EventHandlers {
			tmpString = "\t" + handler.Name + "  " + handler.Reference + "\n"
			gwInfoBuffer.WriteString(tmpString)
		}
	}

	//Links info
	if cType == LINK || cType == ALL {
		links := microgateway.Gateway.EventLinks
		tmpString = fmt.Sprintf("Links: %d\n", len(links))
		gwInfoBuffer.WriteString(tmpString)
		//loop through links
		for _, link := range links {
			gwInfoBuffer.WriteString("\tTrigger: ")
			for _, trigger := range link.Triggers {
				gwInfoBuffer.WriteString(trigger + " ")
			}
			gwInfoBuffer.WriteString("\n")
			gwInfoBuffer.WriteString("\tHandlers:\n")
			for _, dispatcher := range link.Dispatches {
				gwInfoBuffer.WriteString("\t\t" + dispatcher.Handler + "\n")
			}
			gwInfoBuffer.WriteString("\n")
		}
	}

	unLinkedTriggers := 0
	tmpBuf := bytes.NewBufferString("")
	if cType == ALL {
		//Unlinked triggers
		for _, trigger := range microgateway.Gateway.Triggers {
			triggerFound := false
			for _, link := range microgateway.Gateway.EventLinks {
				for _, trigger2 := range link.Triggers {
					if trigger.Name == trigger2 {
						triggerFound = true
						break
					}
				}
				if triggerFound {
					break
				}
			}
			if !triggerFound {
				unLinkedTriggers++
				tmpBuf.WriteString("\t" + trigger.Name + "  " + trigger.Type + "\n")
			}
		}
		if unLinkedTriggers != 0 {
			gwInfoBuffer.WriteString(fmt.Sprintf("Unlinked Triggers: %d", unLinkedTriggers) + "\n")
			gwInfoBuffer.WriteString(tmpBuf.String())
		}

		//Unliked handlers
		unlinkedHandlers := 0
		tmpBuf.Reset()
		for _, handler := range microgateway.Gateway.EventHandlers {
			handlerFound := false
			for _, link := range microgateway.Gateway.EventLinks {
				for _, dispatch := range link.Dispatches {
					if handler.Name == dispatch.Handler {
						handlerFound = true
						break
					}
				}
				if handlerFound {
					break
				}
			}
			if !handlerFound {
				unlinkedHandlers++
				tmpBuf.WriteString("\t" + handler.Name + "  " + handler.Reference + "\n")
			}
		}
		if unlinkedHandlers != 0 {
			gwInfoBuffer.WriteString(fmt.Sprintf("Unlinked Handlers: %d", unlinkedHandlers) + "\n")
			gwInfoBuffer.WriteString(tmpBuf.String())
		}
	}

	return gwInfoBuffer.String(), nil
}

//IsValidGateway validates the gateway schema instance returns bool and error
func IsValidGateway(gatewayJSON string) (bool, error) {

	isValidSchema := false

	suplliedSchemaVersion, err := getSchemaVersion(gatewayJSON)

	if err != nil {
		return isValidSchema, err
	}

	schemaPath, isValidSchema := GetSupportedSchema(suplliedSchemaVersion)

	//check whether CLI supports this schema version
	if !isValidSchema {
		fmt.Printf("Schema version [%v] not supported. Please upgrade mashling cli \n", suplliedSchemaVersion)
		return isValidSchema, nil
	}

	schema, err := assets.Asset(schemaPath)
	if err != nil {
		panic(err.Error())
	}
	schemaString := string(schema)
	schemaLoader := gojsonschema.NewStringLoader(schemaString)
	documentLoader := gojsonschema.NewStringLoader(gatewayJSON)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return false, err
	}

	if result.Valid() {
		isValidSchema = true
	} else {
		fmt.Printf("The gateway json is not valid. See errors:\n")
		for _, desc := range result.Errors() {
			fmt.Printf("- %s\n", desc)
		}
		isValidSchema = false
	}

	return isValidSchema, err

}

func getSchemaVersion(gatewayJSON string) (string, error) {

	suplliedSchema := ""
	gatewayDescriptor := &struct {
		MashlingSchema string `json:"mashling_schema"`
	}{}
	err := json.Unmarshal([]byte(gatewayJSON), gatewayDescriptor)

	if err != nil {
		return suplliedSchema, err
	}
	suplliedSchema = gatewayDescriptor.MashlingSchema

	return suplliedSchema, err
}

// CreateApp creates an application from the specified json application descriptor
func CreateApp(env env.Project, appJSON string, defaultAppFlag bool, rootDir, appName, gatewayJSON string) error {
	return doCreate(env, appJSON, defaultAppFlag, rootDir, appName, gatewayJSON)
}

// CreateApp creates an application from the specified json application descriptor
func doCreate(env env.Project, appJSON string, defaultAppFlag bool, rootDir, appName, gatewayJSON string) error {

	fmt.Print("Creating initial project structure, this might take a few seconds ... \n")

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	err = env.Init(rootDir)
	if err != nil {
		return err
	}

	err = env.Create(false, "")
	if err != nil {
		return err
	}

	err = fgutil.CreateFileFromString(filepath.Join(rootDir, "flogo.json"), appJSON)
	if err != nil {
		return err
	}

	//create the mashling json descriptor file
	err = fgutil.CreateFileFromString(path.Join(rootDir, util.Gateway_Definition_File_Name), gatewayJSON)
	if err != nil {
		return err
	}

	// create initial structure
	appDir := filepath.Join(env.GetSourceDir(), appName)
	os.MkdirAll(appDir, os.ModePerm)

	// Validate structure
	err = env.Open()
	if err != nil {
		return err
	}

	// Create the dep manager
	depManager := &dep.DepManager{Env: env}

	// Initialize the dep manager
	err = depManager.Init()
	if err != nil {
		return err
	}

	// Create initial files
	deps, err := config.ExtractAllDependencies(appJSON)
	if err != nil {
		return err
	}

	CreateMainGoFile(appDir, "")
	CreateImportsGoFile(appDir, deps)

	ensureArgs := []string{}

	if defaultAppFlag {
		//for default app writing dep files
		defGpkgLock := assets.MustAsset("assets/defGopkg.lock")
		defGpkgToml := assets.MustAsset("assets/defGopkg.toml")
		err = ioutil.WriteFile(filepath.Join(env.GetAppDir(), "Gopkg.lock"), defGpkgLock, 0644)
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(filepath.Join(env.GetAppDir(), "Gopkg.toml"), defGpkgToml, 0644)
		if err != nil {
			return err
		}
		ensureArgs = append(ensureArgs, "-vendor-only")
	} else {
		_, gpkgLockErr := os.Stat(filepath.Join(dir, "Gopkg.lock"))
		_, gpkgTomlErr := os.Stat(filepath.Join(dir, "Gopkg.toml"))
		if gpkgLockErr == nil && gpkgTomlErr == nil {
			CopyFile(filepath.Join(dir, "Gopkg.lock"), filepath.Join(env.GetAppDir(), "Gopkg.lock"))
			CopyFile(filepath.Join(dir, "Gopkg.toml"), filepath.Join(env.GetAppDir(), "Gopkg.toml"))
			ensureArgs = append(ensureArgs, "-vendor-only")
		}
	}
	// Sync up
	err = depManager.Ensure(ensureArgs...)
	if err != nil {
		return err
	}

	return nil
}

//PublishToConsul integrates suplied gateway json into consul
func PublishToConsul(gatewayJSON string, addFlag bool, consulToken string, consulDefDir string, consulAddress string) error {

	if !addFlag {
		return DeregisterFromConsul(gatewayJSON, consulToken, consulDefDir, consulAddress)
	}

	return RegisterWithConsul(gatewayJSON, consulToken, consulDefDir, consulAddress)
}

/*
   appendPingFuncionality appends ping triggers, handlers & event_links to given descriptor.
*/
func appendPingDescriptor(pingPort string, descriptor *types.Microgateway) (*types.Microgateway, error) {

	//ping disable value from environment variable
	pingDisableVal := os.Getenv(util.Mashling_Ping_Embed_Config_Property)
	if strings.Compare(pingDisableVal, "TRUE") == 0 {
		if len(pingPort) == 0 {
			pingPort = os.Getenv(util.Mashling_Ping_Port)
			if len(pingPort) == 0 {
				pingPort = util.Mashling_Default_Ping_Port_Val
			}
		}
		pingDescrptr, err := CreateMashlingPingModel(pingPort)
		if err != nil {
			return descriptor, err
		}

		var apendPingFunctionality bool
		apendPingFunctionality = true
		for _, trigger := range pingDescrptr.Gateway.Triggers {

			//check if there are any user defined trigger names, reserved for ping functionality
			for _, descTrigger := range descriptor.Gateway.Triggers {
				if strings.Compare(trigger.Name, descTrigger.Name) == 0 {
					apendPingFunctionality = false
					break
				}
			}

			if apendPingFunctionality {
				descriptor.Gateway.Triggers = append(descriptor.Gateway.Triggers, trigger)
			} else {
				return descriptor, fmt.Errorf("trigger name[%s] is reserved for ping functionality, please use other names for user defined triggers", trigger.Name)
			}
		}
		for _, eventHandlr := range pingDescrptr.Gateway.EventHandlers {
			descriptor.Gateway.EventHandlers = append(descriptor.Gateway.EventHandlers, eventHandlr)
		}
		for _, Config := range pingDescrptr.Gateway.Configurations {
			descriptor.Gateway.Configurations = append(descriptor.Gateway.Configurations, Config)
		}
		for _, eventLink := range pingDescrptr.Gateway.EventLinks {
			descriptor.Gateway.EventLinks = append(descriptor.Gateway.EventLinks, eventLink)
		}
	}
	return descriptor, nil
}

func customizeMainFile(appDir, gatewayName, gatewayJSON string) error {

	flogoLibRev, mashlingRev, err := getProjectRev(appDir)
	if err != nil {
		return err
	}

	// Load the main.go file so we can inject extract meta data output.
	gatewayMain, err := ioutil.ReadFile(filepath.Join(appDir, "main.go"))
	if err != nil {
		return err
	}
	lines := strings.Split(string(gatewayMain), "\n")
	fileContent := ""
	// Create src payload.
	var extraSrc bytes.Buffer
	// Add the ASCII banner.
	banner, err := assets.Asset("assets/banner.txt")
	if err != nil {
		// Asset was not found.
		return err
	}

	schemaVersion, err := getSchemaVersion(gatewayJSON)
	if err != nil {
		return err
	}

	if strings.Compare(os.Getenv(util.Mashling_Ping_Embed_Config_Property), "TRUE") == 0 {
		mashCliOutput := fmt.Sprintf("\n\tmashlingCliRev :=  \"%s\"", MashlingMasterGitRev)
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.MashlingCliRev = mashlingCliRev \n")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprintf("\n\tmashlingCliVersion :=  \"%s\"", Version)
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.MashlingCliVersion = mashlingCliVersion \n")
		extraSrc.WriteString(string(mashCliOutput))

		if DisplayLocalChanges {
			mashCliOutput = fmt.Sprintf("\n\tmashlingLocRev :=  \"%s\"", MashlingLocalGitRev)
			extraSrc.WriteString(string(mashCliOutput))

			mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.MashlingCliLocalRev = mashlingLocRev \n")
			extraSrc.WriteString(string(mashCliOutput))
		}

		mashCliOutput = fmt.Sprint("\n\tappVersion := app.Version")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.AppVersion = appVersion \n")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprintf("\n\tschemaVersion :=  \"%s\"", schemaVersion)
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.SchemaVersion = schemaVersion \n")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprintf("\n\tflogolibRev :=  \"%s\"", flogoLibRev)
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.FlogolibRev = flogolibRev \n")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprintf("\n\tmashlingRev :=  \"%s\"", mashlingRev)
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.MashlingRev = mashlingRev \n")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprintf("\n\tappDesc := app.Description")
		extraSrc.WriteString(string(mashCliOutput))

		mashCliOutput = fmt.Sprint("\n\tutil.PingDataPntr.AppDescrption = appDesc \n\n")
		extraSrc.WriteString(string(mashCliOutput))

	}

	mashlingCliOutput := fmt.Sprintf("\n\tmashlingTxt :=  \"\\n[mashling] mashling CLI version %s\"", Version)
	extraSrc.WriteString(string(mashlingCliOutput))

	mashlingCliOutput = fmt.Sprintf("\n\tmashlingTxt = mashlingTxt + \"\\n[mashling] mashling CLI revision %s\"", MashlingMasterGitRev)
	extraSrc.WriteString(string(mashlingCliOutput))

	if DisplayLocalChanges {
		mashlingCliOutput = fmt.Sprintf("\n\tmashlingTxt = mashlingTxt + \"\\n[mashling] mashling local revision %s\"", MashlingLocalGitRev)
		extraSrc.WriteString(string(mashlingCliOutput))
	}

	mashlingCliOutput = fmt.Sprintf("\n\tmashlingTxt = mashlingTxt + \"\\n\\n\"")
	extraSrc.WriteString(string(mashlingCliOutput))

	mashlingCliOutput = fmt.Sprintf("\n\tfmt.Printf(\"%%s\\n\", mashlingTxt)\n")
	extraSrc.WriteString(string(mashlingCliOutput))

	bannerOutput := fmt.Sprintf("\tbannerTxt := `%s`\n\tfmt.Printf(\"%%s\\n\", bannerTxt)\n", banner)
	extraSrc.WriteString(string(bannerOutput))

	// Append file version output.
	versionOutput := fmt.Sprintf("\tfmt.Printf(\"[mashling] App Version: %%s\\n\", app.Version)\n")
	extraSrc.WriteString(versionOutput)
	// Append schema version output.
	schemaString := fmt.Sprintf("\tfmt.Printf(\"[mashling] Schema Version: %s\\n\")\n", schemaVersion)
	extraSrc.WriteString(schemaString)
	// Append flogo-lib and mashling revisions
	if flogoLibRev != "" {
		flogoLibString := fmt.Sprintf("\tfmt.Printf(\"[mashling] flogo-lib revision: %s\\n\")\n", flogoLibRev)
		extraSrc.WriteString(flogoLibString)
	}
	if mashlingRev != "" {
		mashlingString := fmt.Sprintf("\tfmt.Printf(\"[mashling] mashling revision: %s\\n\")\n", mashlingRev)
		extraSrc.WriteString(mashlingString)
	}
	// Append app description.
	descriptionOutput := fmt.Sprintf("\tfmt.Printf(\"[mashling] App Description: %%s\\n\", app.Description)\n")
	extraSrc.WriteString(descriptionOutput)
	// Cycle through the file contents, inject source, then rewrite the file.
	for _, line := range lines {
		if strings.Contains(line, "e.Start()") {
			fileContent += extraSrc.String()
		}
		fileContent += line
		fileContent += "\n"
	}
	return ioutil.WriteFile(filepath.Join(appDir, "main.go"), []byte(fileContent), 0644)
}

func getProjectRev(appDir string) (string, string, error) {
	//copy gopkg.lock file to gopkglock.toml and load the data into viper object
	err := CopyFile(filepath.Join(appDir, "Gopkg.lock"), filepath.Join(appDir, "Gopkglock.toml"))
	if err != nil {
		return "", "", err
	}

	viper.SetConfigName("Gopkglock") // no need to include file extension
	viper.AddConfigPath(appDir)      // set the path of your config file

	err = viper.ReadInConfig()
	if err != nil {
		fmt.Println("Gopkg Config file not found...")
		os.Remove(filepath.Join(appDir, "Gopkglock.toml"))
		return "", "", err
	}

	err = os.Remove(filepath.Join(appDir, "Gopkglock.toml"))

	if err != nil {
		return "", "", err
	}

	devServer := viper.AllSettings()
	//fmt.Printf("values [%s] \n", devServer)

	test := devServer["projects"]
	projects := test.([]interface{})

	var flogoLibRev, mashlingRev string
	for _, project := range projects {
		projectMap := project.(map[string]interface{})
		if flogoLibRev != "" && mashlingRev != "" {
			break
		} else if strings.Compare(projectMap["name"].(string), "github.com/TIBCOSoftware/flogo-lib") == 0 {
			//fmt.Printf("flogo lib revision [%s]\n", projectMap["revision"])
			flogoLibRev = projectMap["revision"].(string)
		} else if strings.Compare(projectMap["name"].(string), "github.com/TIBCOSoftware/mashling") == 0 {
			//fmt.Printf("mashling revision [%s]\n", projectMap["revision"])
			mashlingRev = projectMap["revision"].(string)
		}
	}
	return flogoLibRev, mashlingRev, err
}
