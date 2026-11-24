-- Яндекс Умный дом: официальный Lua-плагин. Сетевой маршрут берёт у ядра через jarvis.fetch.

local function api_base()
  local custom = jarvis and jarvis.yandex_api
  if type(custom) == "string" and #custom > 0 then
    return (custom:gsub("/+$", "")) .. "/"
  end
  return "https://api.iot.yandex.net/v1.0/"
end

local norm = function(s) return jarvis.norm(tostring(s or "")) end

local function split_words(s)
  local out = {}
  for w in tostring(s or ""):gmatch("%S+") do
    out[#out + 1] = w
  end
  return out
end

local function cps(s)
  local t = {}
  for _, c in utf8.codes(s) do
    t[#t + 1] = c
  end
  return t
end

local function is_cyrillic_lower(s)
  for _, c in utf8.codes(s) do
    if c < 0x0430 or c > 0x044F then
      return false
    end
  end
  return true
end

local function same_word(a, b)
  if a == b then return true end
  local ca, cb = cps(a), cps(b)
  if #ca < 4 or #cb < 4 or #ca ~= #cb then return false end
  for i = 1, #ca - 1 do
    if ca[i] ~= cb[i] then return false end
  end
  return true
end

local function stem(s)
  local c = cps(s)
  local trim = (#c <= 3) and 0 or ((#c <= 5) and 1 or 2)
  if trim == 0 then return s end
  local out = {}
  for i = 1, #c - trim do
    out[i] = utf8.char(c[i])
  end
  return table.concat(out)
end

local function room_match(room, word)
  local want = split_words(norm(word))
  local have = split_words(norm(room))
  if #want == 0 or #want ~= #have then return false end
  for i = 1, #have do
    local a, b = have[i], want[i]
    local st = stem(a)
    if a ~= b and b:sub(1, #st) ~= st then return false end
  end
  return true
end

local function bare(s)
  local n = norm(s)
  local words = split_words(n)
  local skip = { ["эй"] = true, ["слушай"] = true, ["так"] = true, ["ну"] = true, ["джарвис"] = true, ["jarvis"] = true }
  local idx = 1
  while idx <= #words and skip[words[idx]] do
    idx = idx + 1
  end
  if idx == 1 then return n end
  return table.concat(words, " ", idx, #words)
end

local function arr(v)
  return (type(v) == "table") and v or {}
end

local function opt_str(v)
  return (type(v) == "string") and v or ""
end

local function cap_of(d, kind, instance)
  local want = "devices.capabilities." .. kind
  for _, c in ipairs(arr(d.capabilities)) do
    if c.type == want then
      if not instance or instance == "" or (type(c.parameters) == "table" and c.parameters.instance == instance) then
        return c
      end
    end
  end
  return nil
end

local function fmt_num(v)
  if type(v) ~= "number" then return tostring(v) end
  if math.floor(v) == v then return string.format("%.1f", v) end
  return tostring(v)
end

local function describe_device(d)
  local parts = {}
  for _, c in ipairs(arr(d.capabilities)) do
    local p = type(c.parameters) == "table" and c.parameters or {}
    local instance = opt_str(p.instance)
    local kind = opt_str(c.type):gsub("^devices%.capabilities%.", "")
    if kind == "on_off" then
      local state = nil
      if type(c.state) == "table" then state = c.state.value end
      local suffix = state == true and " (сейчас вкл)" or state == false and " (сейчас выкл)" or ""
      parts[#parts + 1] = "вкл/выкл" .. suffix
    elseif kind == "range" then
      local r = type(p.range) == "table" and p.range or {}
      if type(r.min) == "number" and type(r.max) == "number" then
        parts[#parts + 1] = instance .. " " .. fmt_num(r.min) .. ".." .. fmt_num(r.max)
      end
    elseif kind == "color_setting" then
      if type(p.color_model) == "string" and #p.color_model > 0 then
        parts[#parts + 1] = "цвет"
      end
      if type(p.temperature_k) == "table" and type(p.temperature_k.min) == "number" and type(p.temperature_k.max) == "number" then
        if p.temperature_k.max > p.temperature_k.min then
          parts[#parts + 1] = "белый " .. math.floor(p.temperature_k.min) .. ".." .. math.floor(p.temperature_k.max) .. "K"
        end
      end
      if type(p.color_scene) == "table" and type(p.color_scene.scenes) == "table" then
        local ids = {}
        for _, v in ipairs(p.color_scene.scenes) do
          if type(v.id) == "string" then ids[#ids + 1] = v.id end
        end
        parts[#parts + 1] = "сцены " .. table.concat(ids, ",")
      end
    elseif kind == "mode" then
      local vals = {}
      for _, v in ipairs(arr(p.modes)) do
        if type(v.value) == "string" then vals[#vals + 1] = v.value end
      end
      parts[#parts + 1] = "режим " .. instance .. " [" .. table.concat(vals, ",") .. "]"
    elseif kind == "toggle" then
      parts[#parts + 1] = "переключатель " .. instance
    end
  end
  return table.concat(parts, "; ")
end

local function scenario_phrases(s)
  local out = {}
  for _, t in ipairs(arr(s.triggers)) do
    local tr = type(t.trigger) == "table" and t.trigger or nil
    if tr and tr.type == "scenario.trigger.voice" and type(tr.value) == "string" and #tr.value > 0 then
      out[#out + 1] = tr.value
    end
  end
  return out
end

local function hex_rgb(s)
  if type(s) ~= "string" then return nil end
  s = s:match("^%s*(.-)%s*$"):gsub("^#", "")
  if #s ~= 6 or not s:match("^%x%x%x%x%x%x$") then return nil end
  return tonumber(s, 16)
end

local function rgb_hsv(rgb)
  local r = ((rgb >> 16) & 255) / 255.0
  local g = ((rgb >> 8) & 255) / 255.0
  local b = (rgb & 255) / 255.0
  local max = math.max(r, g, b)
  local min = math.min(r, g, b)
  local d = max - min
  local h
  if d == 0.0 then
    h = 0.0
  elseif max == r then
    h = 60.0 * (((g - b) / d) % 6.0)
  elseif max == g then
    h = 60.0 * ((b - r) / d + 2.0)
  else
    h = 60.0 * ((r - g) / d + 4.0)
  end
  local s = (max == 0.0) and 0.0 or (d / max * 100.0)
  local function round_pos(x) return math.floor(x + 0.5) end
  return { h = round_pos(h), s = round_pos(s), v = round_pos(max * 100.0) }
end

local function is_finite(x)
  return type(x) == "number" and x == x and x ~= math.huge and x ~= -math.huge
end

local function bounded(value, p)
  if type(p) == "table" and is_finite(p.min) and is_finite(p.max) and p.min <= p.max then
    if value < p.min then return p.min end
    if value > p.max then return p.max end
  end
  return value
end

local function has_keys(t)
  return type(t) == "table" and next(t) ~= nil
end

local function actions_for(d, a)
  local actions = {}
  local function add(kind, instance, value)
    actions[#actions + 1] = {
      type = "devices.capabilities." .. kind,
      state = { instance = instance, value = value },
    }
  end
  local rgb = hex_rgb(opt_str(a.color))
  if rgb then
    local c = cap_of(d, "color_setting", "")
    local cm = c and type(c.parameters) == "table" and c.parameters.color_model
    if cm == "hsv" then
      add("color_setting", "hsv", rgb_hsv(rgb))
    elseif cm == "rgb" then
      add("color_setting", "rgb", rgb)
    end
  end
  local temp_k = tonumber(a.temperature_k) or 0
  if temp_k > 0 then
    local c = cap_of(d, "color_setting", "")
    if c and type(c.parameters) == "table" and type(c.parameters.temperature_k) == "table" then
      add("color_setting", "temperature_k", math.floor(bounded(temp_k, c.parameters.temperature_k)))
    end
  end
  local brightness = tonumber(a.brightness) or 0
  if brightness > 0 then
    local c = cap_of(d, "range", "brightness")
    if c then
      local pr = type(c.parameters) == "table" and c.parameters.range or nil
      add("range", "brightness", math.floor(bounded(math.min(brightness, 100), pr)))
    end
  end
  if type(a.ranges) == "table" then
    local keys = {}
    for k in pairs(a.ranges) do keys[#keys + 1] = k end
    table.sort(keys)
    for _, instance in ipairs(keys) do
      local value = a.ranges[instance]
      if not (instance == "brightness" and brightness > 0) and is_finite(value) then
        local c = cap_of(d, "range", instance)
        if c then
          local pr = type(c.parameters) == "table" and c.parameters.range or nil
          add("range", instance, bounded(value, pr))
        end
      end
    end
  end
  if type(a.modes) == "table" then
    local keys = {}
    for k in pairs(a.modes) do keys[#keys + 1] = k end
    table.sort(keys)
    for _, instance in ipairs(keys) do
      local value = a.modes[instance]
      local c = cap_of(d, "mode", instance)
      if c and type(c.parameters) == "table" then
        for _, m in ipairs(arr(c.parameters.modes)) do
          if m.value == value then
            add("mode", instance, value)
            break
          end
        end
      end
    end
  end
  if type(a.toggles) == "table" then
    local keys = {}
    for k in pairs(a.toggles) do keys[#keys + 1] = k end
    table.sort(keys)
    for _, instance in ipairs(keys) do
      local val = a.toggles[instance]
      if type(val) == "boolean" and cap_of(d, "toggle", instance) then
        add("toggle", instance, val)
      end
    end
  end
  local on = nil
  if type(a.on) == "boolean" then on = a.on end
  if on == nil and (opt_str(a.color) ~= "" or temp_k > 0 or brightness > 0) and #actions > 0 then
    on = true
  end
  if on ~= nil and cap_of(d, "on_off", "") then
    table.insert(actions, 1, {
      type = "devices.capabilities.on_off",
      state = { instance = "on", value = on },
    })
  end
  return actions
end

local function fully_supported(d, a)
  if type(a.on) == "boolean" and not cap_of(d, "on_off", "") then return false end
  if opt_str(a.color) ~= "" then
    local c = cap_of(d, "color_setting", "")
    local cm = c and type(c.parameters) == "table" and c.parameters.color_model
    if cm ~= "rgb" and cm ~= "hsv" then return false end
  end
  if (tonumber(a.brightness) or 0) > 0 and not cap_of(d, "range", "brightness") then return false end
  if (tonumber(a.temperature_k) or 0) > 0 then
    local c = cap_of(d, "color_setting", "")
    if not (c and type(c.parameters) == "table" and type(c.parameters.temperature_k) == "table") then return false end
  end
  if type(a.ranges) == "table" then
    for k in pairs(a.ranges) do if not cap_of(d, "range", k) then return false end end
  end
  if type(a.toggles) == "table" then
    for k in pairs(a.toggles) do if not cap_of(d, "toggle", k) then return false end end
  end
  if type(a.modes) == "table" then
    for k, v in pairs(a.modes) do
      local c = cap_of(d, "mode", k)
      local ok = false
      if c and type(c.parameters) == "table" then
        for _, m in ipairs(arr(c.parameters.modes)) do
          if m.value == v then ok = true; break end
        end
      end
      if not ok then return false end
    end
  end
  return true
end

local function aliases(kind)
  local k = opt_str(kind):gsub("^devices%.types%.", "")
  if k:sub(1, 14) == "vacuum_cleaner" then
    return { "пылесос", "робот пылесос", "робот", "уборку", "уборка", "пылик" }
  elseif k:sub(1, 13) == "thermostat.ac" then
    return { "кондиционер", "кондей", "кондер" }
  elseif k:sub(1, 10) == "humidifier" then
    return { "увлажнитель" }
  elseif k:sub(1, 8) == "purifier" then
    return { "очиститель" }
  elseif k:sub(1, 15) == "media_device.tv" then
    return { "телевизор", "телек" }
  elseif k:sub(1, 14) == "cooking.kettle" then
    return { "чайник" }
  end
  return {}
end

local function is_light(d)
  if not cap_of(d, "on_off", "") then return false end
  local kind = opt_str(d.type)
  if kind:sub(1, 19) == "devices.types.light" then return true end
  if kind == "devices.types.switch" then
    local name = norm(opt_str(d.name))
    for _, w in ipairs({ "свет", "ламп", "люстр", "лент" }) do
      if name:find(w, 1, true) then return true end
    end
  end
  return false
end

local EVERYWHERE = {
  ["весь свет"] = true, ["все лампы"] = true, ["все освещение"] = true,
  ["свет везде"] = true, ["свет во всем доме"] = true, ["свет во всей квартире"] = true, ["везде свет"] = true,
}

local function split_suffix(name)
  local best_pos, best_pre_len
  for _, sep in ipairs({ " в ", " во ", " на " }) do
    local p = name:find(sep, 1, true)
    if p and (not best_pos or p < best_pos) then
      best_pos = p
      best_pre_len = #sep
    end
  end
  if not best_pos or best_pos <= 1 then return nil end
  local left = name:sub(1, best_pos - 1)
  local right = name:sub(best_pos + best_pre_len)
  if #left == 0 or #right == 0 then return nil end
  return left, right
end

local function plan(c, verb, name, speaker_room)
  if verb == "запусти сценарий" then
    for _, s in ipairs(arr(c.scenarios)) do
      if s.is_active ~= false and norm(opt_str(s.name)) == name then
        return { scenario_id = opt_str(s.id) }
      end
    end
    return nil
  end
  local on = (verb == "включи" or verb == "зажги" or verb == "запусти")
  if name:sub(1, #"устройство ") == "устройство " then
    name = name:sub(#"устройство " + 1)
  elseif name:sub(1, #"группу ") == "группу " then
    name = name:sub(#"группу " + 1)
  end
  local everywhere = EVERYWHERE[name] == true
  local what, scope
  if everywhere then
    what = name
  else
    local left, right = split_suffix(name)
    if left then
      local ids = {}
      for _, r in ipairs(arr(c.rooms)) do
        if room_match(opt_str(r.name), right) then
          ids[#ids + 1] = opt_str(r.id)
        end
      end
      if #ids ~= 1 then return nil end
      what = left
      scope = { [ids[1]] = true }
    else
      what = name
    end
  end
  if what == "свет" or everywhere then
    local room_set = scope or {}
    local room_count = 0
    for _ in pairs(room_set) do room_count = room_count + 1 end
    if not everywhere and room_count == 0 then
      room_set = {}
      if #speaker_room > 0 then
        for _, r in ipairs(arr(c.rooms)) do
          if r.id == speaker_room or room_match(opt_str(r.name), speaker_room) then
            room_set[opt_str(r.id)] = true
          end
        end
      else
        for _, d in ipairs(arr(c.devices)) do
          local rm = opt_str(d.room)
          if opt_str(d.type):sub(1, 27) == "devices.types.smart_speaker" and #rm > 0 then
            room_set[rm] = true
          end
        end
        local cnt = 0
        for _ in pairs(room_set) do cnt = cnt + 1 end
        if cnt ~= 1 then
          room_set = {}
          for _, r in ipairs(arr(c.rooms)) do
            local rn = norm(opt_str(r.name))
            if rn == "зал" or rn == "гостиная" then
              room_set[opt_str(r.id)] = true
            end
          end
        end
      end
      room_count = 0
      for _ in pairs(room_set) do room_count = room_count + 1 end
      if room_count ~= 1 then return nil end
    end
    local lights = {}
    for _, d in ipairs(arr(c.devices)) do
      if is_light(d) and (everywhere or room_set[opt_str(d.room)]) then
        lights[#lights + 1] = d
      end
    end
    if not everywhere then
      local named = {}
      for _, d in ipairs(lights) do
        if norm(opt_str(d.name)) == "свет" then named[#named + 1] = d end
      end
      if #named > 0 then lights = named end
    end
    if #lights == 0 then return nil end
    local ids = {}
    for _, d in ipairs(lights) do ids[#ids + 1] = opt_str(d.id) end
    return { device_ids = ids, on = on }
  end

  local exact, partial = {}, {}
  local what_words = split_words(what)
  local function check(id, label, extra)
    label = norm(label)
    local labels = { label }
    for _, s in ipairs(extra) do labels[#labels + 1] = norm(s) end
    local words = split_words(label)
    if #words > 0 then
      local first = words[1]
      if first:sub(-2) == "а" then
        words[1] = first:sub(1, -3) .. "у"
        labels[#labels + 1] = table.concat(words, " ")
      elseif first:sub(-2) == "я" then
        words[1] = first:sub(1, -3) .. "ю"
        labels[#labels + 1] = table.concat(words, " ")
      end
    end
    local wlen = #split_words(label)
    local matched_exact = false
    for _, s in ipairs(labels) do
      if s == what then
        matched_exact = true
        break
      end
      local sw = split_words(s)
      if #sw == #what_words then
        local all = true
        for i = 1, #sw do
          if not same_word(sw[i], what_words[i]) then all = false; break end
        end
        if all then matched_exact = true; break end
      end
    end
    if matched_exact then
      exact[#exact + 1] = { id = id, len = wlen }
    elseif #what > 0 then
      local lw = split_words(label)
      local all = true
      for _, w in ipairs(what_words) do
        local any = false
        for _, a in ipairs(lw) do
          if same_word(w, a) then any = true; break end
        end
        if not any then all = false; break end
      end
      if all then partial[#partial + 1] = { id = id, len = wlen } end
    end
  end

  for _, d in ipairs(arr(c.devices)) do
    if cap_of(d, "on_off", "") and (not scope or scope[opt_str(d.room)]) then
      local extra = {}
      for _, s in ipairs(aliases(d.type)) do extra[#extra + 1] = s end
      for _, s in ipairs(arr(d.aliases)) do extra[#extra + 1] = s end
      check(opt_str(d.id), opt_str(d.name), extra)
    end
  end
  if not scope then
    for _, g in ipairs(arr(c.groups)) do
      check(opt_str(g.id), opt_str(g.name), arr(g.aliases))
    end
  end
  local hits = (#exact > 0) and exact or partial
  if #hits == 0 then return nil end
  local best = hits[1].len
  for i = 2, #hits do
    if hits[i].len < best then best = hits[i].len end
  end
  local filtered = {}
  for _, h in ipairs(hits) do
    if h.len == best then filtered[#filtered + 1] = h.id end
  end
  if #filtered ~= 1 then return nil end
  return { device_ids = { filtered[1] }, on = on }
end

local COLOR_MAP = {
  ["красн"] = "#FF0000", ["оранжев"] = "#FF7A00", ["желт"] = "#FFD000", ["зелен"] = "#00FF20",
  ["голуб"] = "#00C8FF", ["син"] = "#0030FF", ["фиолетов"] = "#8A00FF", ["розов"] = "#FF3CA0",
}
local TEMP_MAP = { ["бел"] = 4500, ["нейтральн"] = 4500, ["тепл"] = 2700, ["холодн"] = 6500 }
local COLOR_VERBS = { ["сделай"] = true, ["поставь"] = true, ["включи"] = true, ["переключи"] = true, ["измени"] = true, ["смени"] = true }
local COLOR_STEMS = { "красн", "оранжев", "желт", "зелен", "голуб", "син", "фиолетов", "розов", "бел", "тепл", "холодн", "нейтральн" }

local function match_color_stem(w)
  for _, st in ipairs(COLOR_STEMS) do
    if w:sub(1, #st) == st and is_cyrillic_lower(w:sub(#st + 1)) then
      return st
    end
  end
  return nil
end

local function is_color_noun(w)
  for _, st in ipairs({ "цвет", "свет" }) do
    if w:sub(1, #st) == st and is_cyrillic_lower(w:sub(#st + 1)) then
      return true
    end
  end
  return false
end

local function parse_color(n)
  local words = split_words(n)
  if #words < 3 or not COLOR_VERBS[words[1]] then return nil end
  local start_idx = (words[2] == "цвет" and #words >= 4) and 3 or 2
  local color_idx = #words
  local st = match_color_stem(words[color_idx])
  if not st and is_color_noun(words[color_idx]) and color_idx >= start_idx + 1 then
    color_idx = color_idx - 1
    st = match_color_stem(words[color_idx])
  end
  if not st then return nil end
  local end_idx = color_idx - 1
  if end_idx >= start_idx + 1 and words[end_idx] == "на" then
    end_idx = end_idx - 1
  end
  if end_idx < start_idx then return nil end
  return table.concat(words, " ", start_idx, end_idx), st
end

local function parse_command(n)
  for _, v in ipairs({ "запусти сценарий", "включи", "выключи", "погаси", "отключи", "зажги", "запусти", "останови" }) do
    local pfx = v .. " "
    if n:sub(1, #pfx) == pfx then
      local rest = n:sub(#pfx + 1)
      if #rest > 0 then
        local trimmed = rest:match("^(.-)%s+пожалуйста$")
        if trimmed and #trimmed > 0 then rest = trimmed end
        return v, rest
      end
    end
  end
  return nil
end

local function is_route_error(msg)
  msg = tostring(msg or "")
  return msg:find("VPN", 1, true) ~= nil
    or msg:find("SOCKS", 1, true) ~= nil
    or msg:find("маршрут", 1, true) ~= nil
    or msg:find("network_allow", 1, true) ~= nil
    or msg:find("запрещён", 1, true) ~= nil
    or msg:find("хост завершился", 1, true) ~= nil
end

local function room_line(room)
  return "\nКолонка стоит в комнате «" .. room .. "». Если хозяин не назвал комнату («выключи свет», «включи кондиционер») — имеется в виду эта комната; «весь свет» — все лампы и выключатели света во всём доме."
end

local function fallback_context(room, alice)
  local s = alice
    and "\nСписок устройств Яндекса сейчас недоступен: команды умного дома передавай Алисе инструментом alice_command."
    or "\nСписок устройств Яндекса сейчас недоступен. Не утверждай, что действия выполнены."
  if #room > 0 then s = s .. room_line(room) end
  return s
end

local function confirmed(sent, response)
  local devs = arr(response and response.devices)
  local matched = {}
  for _, d in ipairs(devs) do
    if d.id == sent.id then matched[#matched + 1] = d end
  end
  if #matched ~= 1 then return false end
  local result = matched[1]
  local actions = arr(sent.actions)
  if #actions == 0 then return false end
  for _, a in ipairs(actions) do
    local found = {}
    for _, c in ipairs(arr(result.capabilities)) do
      if c.type == a.type and type(c.state) == "table" and type(a.state) == "table" and c.state.instance == a.state.instance then
        found[#found + 1] = c
      end
    end
    if #found ~= 1 then return false end
    local ar = found[1].state and found[1].state.action_result
    if type(ar) ~= "table" or ar.status ~= "DONE" then return false end
  end
  return true
end

local account_state
local function state_for(token)
  if not account_state or account_state.token ~= token then
    account_state = { token = token, catalog = nil, at = 0, epoch = 0 }
  end
  return account_state
end

local function cached_catalog(st, fresh)
  if not st.catalog then return nil end
  if fresh and (jarvis.now() - st.at) >= 30000 then return nil end
  return st.catalog
end

local function invalidate(st)
  st.epoch = st.epoch + 1
  st.catalog = nil
end

local function iot(st, method, path, body, timeout_ms)
  if st.token == "" then error("Яндекс Умный дом не подключён") end
  local segs = {}
  for i = 1, #path do segs[i] = jarvis.urlencode(tostring(path[i])) end
  local url = api_base() .. table.concat(segs, "/")
  local tries = (method == "GET") and 3 or 1
  local headers = {
    Authorization = "Bearer " .. st.token,
    ["Content-Type"] = "application/json",
  }
  local opts = { method = method, headers = headers, timeout_ms = timeout_ms or 10000 }
  if body ~= nil then opts.body = jarvis.json.encode(body) end
  local response
  for attempt = 1, tries do
    local ok, res = pcall(jarvis.fetch, url, opts)
    if ok then
      response = res
      break
    end
    if is_route_error(res) then error(res) end
    if attempt == tries then error("Не удалось связаться с Яндекс Умным домом") end
  end
  if not response.ok then error("Яндекс ответил " .. tostring(response.status)) end
  local ok_json, v = pcall(jarvis.json.decode, response.body)
  if not ok_json or type(v) ~= "table" then error("Яндекс вернул неверный JSON") end
  if v.status ~= "ok" then error("Яндекс не подтвердил запрос") end
  return v
end

local function get_catalog(st, force, timeout_ms)
  if st.token == "" then error("Яндекс Умный дом не подключён") end
  if not force then
    local c = cached_catalog(st, true)
    if c then return c end
  end
  local epoch = st.epoch
  local ok, res = pcall(iot, st, "GET", { "user", "info" }, nil, timeout_ms)
  if ok then
    if st.epoch == epoch then
      st.catalog = res
      st.at = jarvis.now()
    end
    return res
  end
  if not force and not is_route_error(res) then
    local stale = cached_catalog(st, false)
    if stale then return stale end
  end
  error(res)
end

local function apply_args(st, a, alice)
  local color = opt_str(a.color)
  if #color > 0 and not hex_rgb(color) then
    return "Неверный цвет: нужен #rrggbb."
  end
  local ok_cat, c = pcall(get_catalog, st, false)
  if not ok_cat then
    if is_route_error(c) then error(c) end
    return "Не удалось связаться с Яндекс Умным домом."
  end
  local scenario_id = opt_str(a.scenario_id)
  if #scenario_id > 0 then
    local active = false
    for _, s in ipairs(arr(c.scenarios)) do
      if s.id == scenario_id and s.is_active ~= false then active = true; break end
    end
    if not active then return "Не нашёл активный сценарий." end
    local ok_sc, err_sc = pcall(iot, st, "POST", { "scenarios", scenario_id, "actions" }, nil)
    if not ok_sc and is_route_error(err_sc) then error(err_sc) end
    invalidate(st)
    return ok_sc and "Готово." or "Сценарий не запустился."
  end
  local raw_ids = arr(a.device_ids)
  if #raw_ids == 0 then
    return alice
      and "Не указано, какие устройства. Если нужного устройства нет в списке — передай команду Алисе инструментом alice_command."
      or "Не указано, какие устройства."
  end
  local id_set = {}
  for _, id in ipairs(raw_ids) do
    if type(id) == "string" then id_set[id] = true end
  end
  local unique_ids = {}
  for id in pairs(id_set) do unique_ids[#unique_ids + 1] = id end
  table.sort(unique_ids)

  local failed, unsupported, attempted, partial, unknown = 0, 0, 0, false, 0
  local devices = {}
  local dev_map, grp_map = {}, {}
  for _, d in ipairs(arr(c.devices)) do dev_map[opt_str(d.id)] = d end
  for _, g in ipairs(arr(c.groups)) do grp_map[opt_str(g.id)] = g end

  for _, id in ipairs(unique_ids) do
    local group = grp_map[id]
    if group then
      local gdevs = arr(group.devices)
      local only_on = type(a.on) == "boolean"
        and color == ""
        and (tonumber(a.temperature_k) or 0) <= 0
        and (tonumber(a.brightness) or 0) <= 0
        and not has_keys(a.ranges)
        and not has_keys(a.modes)
        and not has_keys(a.toggles)
      if not only_on or #gdevs == 0 then
        unsupported = unsupported + 1
      else
        attempted = attempted + 1
        local ok_g, resp_g = pcall(iot, st, "POST", { "groups", id, "actions" }, {
          actions = { { type = "devices.capabilities.on_off", state = { instance = "on", value = a.on } } },
        })
        if not ok_g and is_route_error(resp_g) then error(resp_g) end
        local all_ok = ok_g and type(resp_g) == "table" and #arr(resp_g.devices) == #gdevs
        if all_ok then
          for _, member_id in ipairs(gdevs) do
            if not confirmed({
              id = member_id,
              actions = { { type = "devices.capabilities.on_off", state = { instance = "on" } } },
            }, resp_g) then
              all_ok = false
              break
            end
          end
        end
        if not all_ok then failed = failed + 1 end
      end
    else
      local d = dev_map[id]
      if not d then
        unknown = unknown + 1
      else
        local actions = actions_for(d, a)
        if #actions == 0 then
          unsupported = unsupported + 1
        else
          if not fully_supported(d, a) then partial = true end
          attempted = attempted + 1
          devices[#devices + 1] = { id = id, actions = actions }
        end
      end
    end
  end
  if #devices > 0 then
    local ok_d, resp_d = pcall(iot, st, "POST", { "devices", "actions" }, { devices = devices })
    if ok_d then
      for _, sent in ipairs(devices) do
        if not confirmed(sent, resp_d) then failed = failed + 1 end
      end
    else
      if is_route_error(resp_d) then error(resp_d) end
      invalidate(st)
      return "Не удалось связаться с Яндекс Умным домом. Выполнение не подтверждено."
    end
  end
  if attempted > 0 then invalidate(st) end
  if attempted == 0 then
    return (unsupported > 0) and "Эти устройства так не умеют." or "Не нашёл такие устройства."
  end
  if failed == 0 and unsupported == 0 and unknown == 0 and not partial then
    return "Готово."
  elseif failed < attempted and (unsupported > 0 or unknown > 0 or partial) then
    return "Часть действий выполнена; не все устройства найдены или поддерживают выбранные параметры."
  elseif failed < attempted then
    return "Готово, но " .. failed .. " из " .. attempted .. " не ответили."
  else
    return "Яндекс не подтвердил выполнение всех запрошенных действий."
  end
end

local function build_context(st, room, alice)
  local ok, c = pcall(get_catalog, st, false, 4000)
  if not ok then return fallback_context(room, alice) end
  local rooms = {}
  for _, r in ipairs(arr(c.rooms)) do rooms[opt_str(r.id)] = opt_str(r.name) end
  local lines = {}
  local speaker_room = ""
  for _, d in ipairs(arr(c.devices)) do
    local kind = opt_str(d.type)
    if kind:sub(1, 27) == "devices.types.smart_speaker" then
      local rn = rooms[opt_str(d.room)]
      if rn then speaker_room = rn end
    elseif kind:sub(1, 20) ~= "devices.types.sensor" then
      local rname = rooms[opt_str(d.room)] or "—"
      local short_kind = kind:gsub("^devices%.types%.", "")
      lines[#lines + 1] = "\n- " .. opt_str(d.id) .. " «" .. opt_str(d.name) .. "» (" .. short_kind .. ", " .. rname .. "): " .. describe_device(d)
    end
  end
  table.sort(lines)
  local parts = { "\n\nУмный дом хозяина (в инструмент smart_home передавай id):", table.concat(lines) }
  for _, g in ipairs(arr(c.groups)) do
    parts[#parts + 1] = "\n- группа " .. opt_str(g.id) .. " «" .. opt_str(g.name) .. "»"
  end
  for _, s in ipairs(arr(c.scenarios)) do
    if s.is_active ~= false then
      parts[#parts + 1] = "\n- сценарий " .. opt_str(s.id) .. " «" .. opt_str(s.name) .. "», фразы: " .. table.concat(scenario_phrases(s), " / ")
    end
  end
  if #room > 0 then speaker_room = room end
  if speaker_room == "" then speaker_room = "Зал" end
  parts[#parts + 1] = room_line(speaker_room)
  return table.concat(parts)
end

local function catalog_view(c)
  local rooms, devices, scenarios = {}, {}, {}
  for _, r in ipairs(arr(c.rooms)) do
    rooms[#rooms + 1] = { id = opt_str(r.id), name = opt_str(r.name) }
  end
  for _, d in ipairs(arr(c.devices)) do
    devices[#devices + 1] = {
      id = opt_str(d.id), name = opt_str(d.name), room = opt_str(d.room),
      type = opt_str(d.type), description = describe_device(d),
    }
  end
  for _, s in ipairs(arr(c.scenarios)) do
    local item = { id = opt_str(s.id), name = opt_str(s.name), phrases = scenario_phrases(s) }
    if type(s.is_active) == "boolean" then item.is_active = s.is_active end
    scenarios[#scenarios + 1] = item
  end
  return { rooms = rooms, devices = devices, scenarios = scenarios }
end

local function scenario_matches(s, n)
  if s.is_active == false then return false end
  for _, ph in ipairs(scenario_phrases(s)) do
    local np = norm(ph)
    if #np > 0 and np == n then return true end
  end
  local nn = norm(opt_str(s.name))
  return #nn > 0 and nn == n
end

local function fast_cmd(st, text, room, alice)
  local n = bare(text)
  local ok, c = pcall(get_catalog, st, false)
  if not ok then
    if is_route_error(c) then return nil end
    return nil
  end
  for _, s in ipairs(arr(c.scenarios)) do
    if scenario_matches(s, n) then
      return apply_args(st, { scenario_id = opt_str(s.id) }, alice)
    end
  end
  local target, cstem = parse_color(n)
  if target then
    local p = plan(c, "включи", target, room)
    if not p then return nil end
    local a = {
      color = COLOR_MAP[cstem] or "",
      temperature_k = TEMP_MAP[cstem] or 0,
    }
    local filtered = {}
    for _, id in ipairs(p.device_ids) do
      for _, d in ipairs(arr(c.devices)) do
        if d.id == id and #actions_for(d, a) > 0 then
          filtered[#filtered + 1] = id
          break
        end
      end
    end
    if #filtered == 0 then return nil end
    a.device_ids = filtered
    return apply_args(st, a, alice)
  end
  local verb, rest = parse_command(n)
  if not verb then return nil end
  local a = plan(c, verb, rest, room)
  if not a then return nil end
  return apply_args(st, a, alice)
end

return {
  service = function(req)
    assert(req.method == "yandexhome", "Неизвестный сервис")
    local p = req.params or {}
    local token = opt_str(p.token):match("^%s*(.-)%s*$")
    local op = opt_str(p.op)
    local room = opt_str(p.room):match("^%s*(.-)%s*$")
    local alice = p.alice == true
    local st = state_for(token)
    if op == "has-catalog" then
      local c = cached_catalog(st, false)
      return c ~= nil and (#arr(c.devices) > 0 or #arr(c.scenarios) > 0)
    elseif op == "invalidate" then
      invalidate(st)
      return true
    end
    if jarvis.network_check then
      local ok_net, err_net = pcall(jarvis.network_check)
      if not ok_net then
        if op == "context" then return fallback_context(room, alice) end
        if op == "fast" then return { handled = false, answer = "" } end
        error(tostring(err_net))
      end
    end
    if op == "settings.validate" then
      get_catalog(st, true)
      return true
    elseif op == "catalog" then
      return catalog_view(get_catalog(st, p.args == true))
    elseif op == "prewarm" then
      local ok, err = pcall(get_catalog, st, false, 6000)
      if not ok and is_route_error(err) then error(err) end
      return true
    elseif op == "context" then
      return build_context(st, room, alice)
    elseif op == "scenario-match" then
      local ok, c = pcall(get_catalog, st, false)
      if not ok then return false end
      local n = bare(opt_str(p.args))
      for _, s in ipairs(arr(c.scenarios)) do
        if scenario_matches(s, n) then return true end
      end
      return false
    elseif op == "fast" then
      local ans = fast_cmd(st, opt_str(p.args), room, alice)
      if ans then return { handled = true, answer = ans } end
      return { handled = false, answer = "" }
    elseif op == "control" then
      local ok, args = pcall(jarvis.json.decode, opt_str(p.args))
      if not ok or type(args) ~= "table" then return "Не понял команду." end
      return apply_args(st, args, alice)
    end
    error("Неизвестная команда Яндекс Умного дома")
  end,
}
