local codes={[0]='ясно',[1]='в основном ясно',[2]='переменная облачность',[3]='пасмурно',[45]='туман',[48]='изморозь и туман',[51]='слабая морось',[53]='морось',[55]='сильная морось',[56]='ледяная морось',[57]='сильная ледяная морось',[61]='небольшой дождь',[63]='дождь',[65]='сильный дождь',[66]='ледяной дождь',[67]='сильный ледяной дождь',[71]='небольшой снег',[73]='снег',[75]='сильный снег',[77]='снежная крупа',[80]='ливень',[81]='ливни',[82]='сильные ливни',[85]='снегопад',[86]='сильный снегопад',[95]='гроза',[96]='гроза с градом',[99]='сильная гроза с градом'}
local function round(n) return math.floor(n+0.5) end
local function signed(n) n=round(n);return (n>0 and '+' or '')..n end
local function deg(n)
  local a=math.abs(round(n))%100;local b=a%10
  return signed(n)..' '..((a>10 and a<20) and 'градусов' or b==1 and 'градус' or (b>=2 and b<=4) and 'градуса' or 'градусов')
end
local function degof(n) local a=math.abs(round(n))%100;return signed(n)..((a%10==1 and a~=11) and ' градуса' or ' градусов') end
local function get(url)
  local response=jarvis.fetch(url);assert(response.ok,'Сервис погоды ответил '..response.status)
  return jarvis.json.decode(response.body)
end
local function place(city)
  local name=city or '';if name=='' then name=jarvis.settings.city or '' end;if name=='' then name=jarvis.info.city or '' end
  name=name:match('^%s*(.-)%s*$');assert(name~='','Укажите город в настройках ассистента или плагина')
  local key='geo:'..jarvis.normalize(name);local cached=jarvis.store.get(key);if cached then return cached end
  local data=get('https://geocoding-api.open-meteo.com/v1/search?count=1&language=ru&name='..jarvis.urlencode(name))
  local hit=data.results and data.results[1];assert(hit,'Не нашёл город «'..name..'»')
  local result={name=hit.name,lat=hit.latitude,lon=hit.longitude};jarvis.store.set(key,result);return result
end
local function forecast(city,day)
  local p=place(city)
  local d=get('https://api.open-meteo.com/v1/forecast?latitude='..p.lat..'&longitude='..p.lon..'&current=temperature_2m,apparent_temperature,weather_code,wind_speed_10m&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max&timezone=auto&forecast_days=3&wind_speed_unit=ms')
  local i=({today=1,tomorrow=2,after_tomorrow=3})[day or 'today'] or 1;local daily=d.daily;local text
  if i==1 and d.current then
    local c=d.current;text=p.name..': сейчас '..deg(c.temperature_2m)..', '..(codes[c.weather_code] or 'без осадков')..', ощущается как '..deg(c.apparent_temperature)..', ветер '..round(c.wind_speed_10m)..' м/с. Днём до '..degof(daily.temperature_2m_max[1])..', ночью до '..degof(daily.temperature_2m_min[1])..'.'
  else text=p.name..(i==2 and ' завтра: ' or ' послезавтра: ')..(codes[daily.weather_code[i]] or 'без осадков')..', от '..signed(daily.temperature_2m_min[i])..' до '..degof(daily.temperature_2m_max[i])..'.' end
  local rain=tonumber(daily.precipitation_probability_max[i]);if rain then if rain>=50 then text=text..' Вероятность осадков '..rain..'%, зонт пригодится.' elseif rain>=20 then text=text..' Осадки возможны: '..rain..'%.' end end
  return text
end
local commands={}
for _,prefix in ipairs({'','какая ','что за ','что с ','что там с '}) do for _,word in ipairs({'погода','погоду','погоды'}) do for _,suffix in ipairs({'',' сейчас',' сегодня',' завтра',' послезавтра',' на улице'}) do commands[prefix..word..suffix]=true end end end
for _,prefix in ipairs({'','сколько ','какая '}) do for _,word in ipairs({'градусов','температура'}) do for _,suffix in ipairs({'',' на улице',' сейчас',' завтра'}) do commands[prefix..word..suffix]=true end end end
for _,prefix in ipairs({'нужен ли','брать ли'}) do for _,suffix in ipairs({'',' сегодня',' завтра'}) do commands[prefix..' зонт'..suffix]=true end end
local function status() jarvis.status('город: '..(jarvis.settings.city or jarvis.info.city or 'не задан')) end
return {
  init=status,configure=status,
  tools={{name='get_weather',description='Погода сейчас и прогноз на сегодня, завтра или послезавтра. Город не назвали — не передавай его.',parameters={type='object',properties={city={type='string'},day={type='string',enum={'today','tomorrow','after_tomorrow'}}}},handler=function(a) return forecast(a.city,a.day) end}},
  command=function(text)
    if jarvis.settings.fast==false or not commands[text] then return end
    return forecast('',text:find('послезавтра',1,true) and 'after_tomorrow' or text:find('завтра',1,true) and 'tomorrow' or 'today')
  end,
}
