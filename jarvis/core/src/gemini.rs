use serde_json::{Value, json};

pub(crate) fn declarations(tools: &[Value]) -> Vec<Value> {
    tools
        .iter()
        .filter(|tool| tool["type"] == "function")
        .map(|tool| {
            let mut declaration = json!({"name": tool["name"], "description": tool["description"]});
            if let Some(schema) = tool.get("parameters").filter(|schema| schema.is_object()) {
                // Gemini's typed `parameters` field rejects JSON Schema keywords such as
                // additionalProperties. Keep the original schemas in its JSON Schema field.
                declaration["parametersJsonSchema"] = schema.clone();
            }
            declaration
        })
        .collect()
}

pub(crate) fn live_setup(model: &str, voice: &str, instructions: &str, language: &str, tools: &[Value], web_search: bool) -> Value {
    let mut setup = json!({
        "model": format!("models/{model}"),
        "generationConfig": {
            "responseModalities": ["AUDIO"],
            "speechConfig": {"voiceConfig": {"prebuiltVoiceConfig": {"voiceName": voice}}}
        },
        "systemInstruction": {"parts": [{"text": instructions}]},
        "realtimeInputConfig": {
            "automaticActivityDetection": {
                "endOfSpeechSensitivity": "END_SENSITIVITY_LOW",
                "silenceDurationMs": 750
            }
        },
        "inputAudioTranscription": {},
        "outputAudioTranscription": {}
    });
    let language_code = match language {
        "ru" => Some("ru-RU"),
        "en" => Some("en-US"),
        "uk" => Some("uk-UA"),
        _ => None,
    };
    if let Some(code) = language_code {
        setup["inputAudioTranscription"]["languageCodes"] = json!([code]);
    }
    let functions = declarations(tools);
    let mut enabled = Vec::new();
    if !functions.is_empty() {
        enabled.push(json!({"functionDeclarations": functions}));
    }
    if web_search {
        enabled.push(json!({"googleSearch": {}}));
    }
    if !enabled.is_empty() {
        setup["tools"] = json!(enabled);
    }
    json!({"setup": setup})
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn live_setup_keeps_nested_json_schema_constraints() {
        let schema = json!({
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "aliases": {"type": "object", "additionalProperties": {"type": "string"}}
            },
            "required": ["aliases"]
        });
        let setup = live_setup(
            "model",
            "voice",
            "instructions",
            "ru",
            &[
                json!({"type": "function", "name": "edit_memory", "description": "Edit", "parameters": schema}),
                json!({"type": "web_search"}),
            ],
            true,
        );
        let functions = setup["setup"]["tools"][0]["functionDeclarations"]
            .as_array()
            .unwrap();
        assert_eq!(functions.len(), 1);
        assert_eq!(functions[0]["parametersJsonSchema"], schema);
        assert!(functions[0].get("parameters").is_none());
        assert_eq!(setup["setup"]["tools"][1], json!({"googleSearch": {}}));
        assert_eq!(setup["setup"]["inputAudioTranscription"], json!({"languageCodes": ["ru-RU"]}));
        assert_eq!(setup["setup"]["realtimeInputConfig"]["automaticActivityDetection"]["silenceDurationMs"], 750);
        assert!(
            live_setup("model", "voice", "instructions", "", &[], false)["setup"]
                .get("tools")
                .is_none()
        );
        assert_eq!(
            live_setup("model", "voice", "instructions", "", &[], false)["setup"]["inputAudioTranscription"],
            json!({}),
        );
    }
}
