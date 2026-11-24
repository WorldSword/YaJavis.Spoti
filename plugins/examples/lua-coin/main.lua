local function flip()
  return math.random(2) == 1 and jarvis.settings.heads or 'решка'
end

return {
  init = function() jarvis.status('Готов подбросить монетку') end,
  tools = {{
    name = 'flip_coin',
    description = 'Подбросить монетку',
    parameters = {type = 'object', properties = {}},
    handler = function(args) return flip() end,
  }},
  command = function(text)
    if text == 'подбрось монетку' or text == 'брось монетку' then
      return 'Выпало: ' .. flip()
    end
    return nil
  end,
}
