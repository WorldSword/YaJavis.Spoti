local function random(a)
  local lo,hi=tonumber(a.min),tonumber(a.max)
  assert(lo and hi and lo%1==0 and hi%1==0,'Нужны целые min и max')
  return tostring(jarvis.random(math.min(lo,hi),math.max(lo,hi)))
end
return {
  tools={{name='random_number',description='Случайное целое число в диапазоне: жребий, кубик, лотерея.',parameters={type='object',properties={min={type='integer'},max={type='integer'}},required={'min','max'}},handler=random}},
  command=function(text)
    for _,verb in ipairs({'брось','подбрось','кинь'}) do
      for _,noun in ipairs({'монетку','монету'}) do
        if text==verb..' '..noun then return jarvis.random(0,1)==0 and 'Орёл.' or 'Решка.' end
      end
    end
    if text=='орел или решка' then return jarvis.random(0,1)==0 and 'Орёл.' or 'Решка.' end
    for _,verb in ipairs({'брось','кинь'}) do
      for _,noun in ipairs({'кубик','кости'}) do if text==verb..' '..noun then return 'Выпало '..jarvis.random(1,6)..'.' end end
    end
    for _,verb in ipairs({'загадай','выбери','назови'}) do
      local tail=text:match('^'..verb..' (.+)$')
      if tail then
        tail=tail:gsub('^случайное ','')
        local a,b=tail:match('^число от (%d+) до (%d+)$')
        if a and tonumber(b)>tonumber(a) then return 'Пусть будет '..random({min=a,max=b})..'.' end
      end
    end
  end,
}
