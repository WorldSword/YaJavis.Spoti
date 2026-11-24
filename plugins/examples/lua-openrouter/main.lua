return {
  init = function()
    local key = jarvis.settings.api_key or ""
    jarvis.status(key ~= "" and "Готов" or "Укажите API-ключ OpenRouter")
  end,

  configure = function(settings)
    local key = settings.api_key or ""
    jarvis.status(key ~= "" and "Готов" or "Укажите API-ключ OpenRouter")
  end,

  chat = function(request)
    local key = jarvis.settings.api_key or ""
    if key == "" then
      error("Укажите API-ключ OpenRouter в настройках плагина")
    end

    local model = request.model
    if not model or model == "" then
      model = jarvis.settings.model
    end
    if not model or model == "" then
      model = "deepseek/deepseek-chat-v3-0324"
    end

    local payload = {
      model = model,
      messages = request.messages,
    }
    if request.tools and #request.tools > 0 then
      payload.tools = request.tools
    end

    local response = jarvis.fetch("https://openrouter.ai/api/v1/chat/completions", {
      method = "POST",
      headers = {
        ["Authorization"] = "Bearer " .. key,
        ["Content-Type"] = "application/json",
        ["X-Title"] = "YaJarvis",
      },
      body = jarvis.json.encode(payload),
    })

    if not response.ok then
      error("OpenRouter HTTP " .. tostring(response.status) .. ": " .. string.sub(response.body or "", 1, 200))
    end

    local data = jarvis.json.decode(response.body)
    if data.error and data.error.message then
      error("OpenRouter: " .. tostring(data.error.message))
    end

    local message = data.choices and data.choices[1] and data.choices[1].message
    if not message then
      error("OpenRouter вернул пустой ответ")
    end

    if message.tool_calls and #message.tool_calls > 0 then
      local calls = {}
      for i, call in ipairs(message.tool_calls) do
        local fn = call["function"] or {}
        calls[i] = {
          id = call.id or ("call_" .. tostring(i)),
          name = fn.name,
          arguments = type(fn.arguments) == "string" and fn.arguments or jarvis.json.encode(fn.arguments or {}),
        }
      end
      return {
        text = type(message.content) == "string" and message.content or "",
        tool_calls = calls,
      }
    end

    local content = message.content
    if type(content) ~= "string" or content == "" then
      error("OpenRouter вернул пустое сообщение")
    end
    return content
  end,
}
