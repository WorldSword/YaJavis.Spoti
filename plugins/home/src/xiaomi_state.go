package main

func (x *Xiaomi) state() map[string]any {
	if x == nil {
		return map[string]any{}
	}
	a := x.store.Get().Xiaomi
	st := map[string]any{"signed_in": a.SignedIn(), "region": a.Region, "login": x.loginState()}
	var devs []map[string]any
	for _, d := range x.Devices() {
		devs = append(devs, map[string]any{"name": d.Name, "room": d.Room, "type": d.Type, "online": d.Online, "model": d.Model, "region": d.Region})
	}
	st["devices"] = devs
	return st
}
