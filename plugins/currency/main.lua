local names={USD={'доллар','бакс'},EUR={'евро'},CNY={'юан'},GBP={'фунт'},JPY={'иен','йен'},CHF={'франк'},TRY={'лир'},KZT={'тенге'},BYN={'белорусск'},RUB={'российск','рубл'},UAH={'грив'},PLN={'злот'}}
local function code(word)
  word=jarvis.normalize(word or '')
  if word:match('^[a-z][a-z][a-z]$') then return word:upper() end
  for id,stems in pairs(names) do for _,stem in ipairs(stems) do if word:find(stem,1,true) then return id end end end
end
local function get(url)
  local response=jarvis.fetch(url)
  assert(response.ok,'Банк ответил '..response.status)
  return jarvis.json.decode(response.body)
end
local function rates()
  local bank=jarvis.settings.bank or 'ЦБ РФ'
  local key='rates:'..bank
  local cached=jarvis.store.get(key)
  if cached and jarvis.now()-cached.at<3600000 then return cached end
  local out={r={},at=jarvis.now()}
  if bank=='НБ РБ' then
    out.base='BYN';out.unit='белорусских рублей'
    for _,x in ipairs(get('https://api.nbrb.by/exrates/rates?periodicity=0')) do out.r[x.Cur_Abbreviation]=x.Cur_OfficialRate/x.Cur_Scale end
  else
    out.base='RUB';out.unit='рублей'
    for id,v in pairs(get('https://www.cbr-xml-daily.ru/daily_json.js').Valute) do out.r[id]=v.Value/v.Nominal end
  end
  jarvis.store.set(key,out)
  return out
end
local function convert(cur,amount)
  local id=code(cur)
  if not id then return 'Не знаю такую валюту: '..tostring(cur) end
  local r=rates()
  if id==r.base then return 'Это и есть местная валюта.' end
  if not r.r[id] then return 'Банк не даёт курс '..id..'.' end
  local n=tonumber(amount) or 1;if n<=0 then n=1 end
  local value=n*r.r[id];local precision=value<10 and 10000 or 100
  local text=tostring(math.floor(value*precision+0.5)/precision):gsub('%.',',')
  return n..' '..id..' = '..text..' '..r.unit..' ('..(jarvis.settings.bank or 'ЦБ РФ')..').'
end
return {
  tools={{name='exchange_rate',description='Официальный курс валюты к рублю (ЦБ РФ или НБ РБ) и пересчёт суммы.',parameters={type='object',properties={currency={type='string'},amount={type='number'}},required={'currency'}},handler=function(a) return convert(a.currency,a.amount) end}},
  command=function(text)
    local tail=text:match('^курс (.+)$') or text:match('^какой курс (.+)$') or text:match('^сейчас курс (.+)$') or text:match('^сколько стоит (.+)$')
    if tail then tail=tail:gsub(' сегодня$',''):gsub(' сейчас$','');if code(tail) then return convert(tail,1) end end
    local n,word=text:match('^сколько (%d+) (.+)$')
    if word then word=word:gsub(' в рублях$','');if code(word) then return convert(word,tonumber(n)) end end
  end,
}
