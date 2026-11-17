package main

func (t *SmartLife) state() map[string]any {
	if t == nil {
		return map[string]any{}
	}
	st := map[string]any{"signed_in": t.store.Get().Tuya.SignedIn(), "login": t.loginState()}
	var devs []map[string]any
	for _, d := range t.Devices() {
		local := d.LocalKey != "" && t.lan != nil && t.lan.Known(d.ID)
		devs = append(devs, map[string]any{"name": d.Name, "room": d.Room, "product": d.Product, "online": d.Online, "local": local})
	}
	st["devices"] = devs
	return st
}
