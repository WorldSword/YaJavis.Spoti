package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// recommendationRequest only matches a playback request, never a general question.
func recommendationRequest(text string) bool {
	t := leadRe.ReplaceAllString(normText(text), "")
	if strings.HasPrefix(t, "рекомендац") || strings.HasPrefix(t, "рекомендован") || strings.HasPrefix(t, "что нибудь ") {
		return true
	}
	for _, s := range []string{"рекомендации youtube", "рекомендации ютуб", "рекомендации ютуба", "рекомендованные", "рекомендованные видео", "мои рекомендации", "что нибудь интересное", "что то интересное"} {
		if t == s {
			return true
		}
	}
	verb := false
	for _, v := range []string{"включи ", "поставь ", "запусти ", "давай ", "найди ", "подбери ", "выбери ", "предложи "} {
		if strings.HasPrefix(t, v) {
			verb = true
		}
	}
	if !verb {
		return false
	}
	for _, s := range []string{"мои рекомендации", "моих рекомендац", "из рекомендац", "что нибудь", "что то интересное", "послушать", "чтобы болтало на фоне", "сам ", "самому", "на свой вкус"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	if strings.HasPrefix(t, "подбери ") || strings.HasPrefix(t, "выбери ") || strings.HasPrefix(t, "предложи ") {
		return true
	}
	return false
}

func (m *Music) recommendations(ctx context.Context) ([]Track, error) {
	if m.Account == nil {
		return nil, errors.New("для личных рекомендаций войди в YouTube в разделе «Подключения»")
	}
	tok, err := m.Account.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("вход YouTube: %w", err)
	}
	if tok == "" {
		return nil, errors.New("для личных рекомендаций войди в YouTube в разделе «Подключения»")
	}
	const browseVersion = "7.20260114.12.00"
	body := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "TVHTML5", "clientVersion": browseVersion, "hl": "ru", "gl": "RU", "platform": "TV", "deviceMake": "Samsung", "deviceModel": "SmartTV"}}, "browseId": "FEwhat_to_watch"}
	api := &Music{client: m.Account.client}
	out, err := api.post(ctx, "https://www.youtube.com/youtubei/v1/browse?prettyPrint=false", body, ytTVAgent,
		"Authorization", "Bearer "+tok, "X-YouTube-Client-Name", "7", "X-YouTube-Client-Version", browseVersion)
	if err != nil {
		return nil, err
	}
	if e, ok := out["error"].(map[string]any); ok {
		return nil, fmt.Errorf("рекомендации YouTube: %s", str(e["message"]))
	}
	if rc, ok := out["responseContext"].(map[string]any); ok {
		if web, ok := rc["mainAppWebResponseContext"].(map[string]any); ok && web["loggedOut"] == true {
			return nil, errors.New("YouTube не принял вход для личных рекомендаций — войди заново")
		}
	}
	tracks := recommendationTracks(out["contents"])
	if len(tracks) == 0 {
		if os.Getenv("JARVIS_RECOMMEND_DEBUG") != "" {
			b, _ := json.Marshal(out)
			fmt.Printf("home response: %.9000s\n", b)
		}
		return nil, errors.New("YouTube пока не вернул личную ленту рекомендаций")
	}
	return tracks, nil
}

func recommendationTracks(root any) []Track {
	var tracks []Track
	seen := map[string]bool{}
	var visit func(any)
	visit = func(v any) {
		if len(tracks) >= 60 {
			return
		}
		switch o := v.(type) {
		case []any:
			for _, x := range o {
				visit(x)
			}
		case map[string]any:
			for _, key := range []string{"videoRenderer", "gridVideoRenderer", "compactVideoRenderer", "tileRenderer", "lockupViewModel"} {
				if card, ok := o[key].(map[string]any); ok {
					t := Track{ID: str(card["videoId"]), Title: youtubeText(card["title"]), Author: youtubeText(card["shortBylineText"])}
					if t.Author == "" {
						t.Author = youtubeText(card["ownerText"])
					}
					if key == "lockupViewModel" {
						if str(card["contentType"]) != "LOCKUP_CONTENT_TYPE_VIDEO" {
							return
						}
						t.ID = str(card["contentId"])
						meta, _ := card["metadata"].(map[string]any)
						view, _ := meta["lockupMetadataViewModel"].(map[string]any)
						title, _ := view["title"].(map[string]any)
						t.Title = str(title["content"])
						walk(view["metadata"], func(k string, x map[string]any) {
							if k == "text" && t.Author == "" {
								t.Author = str(x["content"])
							}
						})
					}
					if key == "tileRenderer" {
						ad := false
						walk(card, func(k string, x map[string]any) {
							if k == "adBadgeViewModel" {
								ad = true
							}
							if k == "watchEndpoint" && t.ID == "" {
								t.ID = str(x["videoId"])
							}
							if k == "tileMetadataRenderer" {
								t.Title = youtubeText(x["title"])
								lines, _ := x["lines"].([]any)
								if len(lines) > 0 {
									line, _ := lines[0].(map[string]any)
									r, _ := line["lineRenderer"].(map[string]any)
									items, _ := r["items"].([]any)
									if len(items) > 0 {
										item, _ := items[0].(map[string]any)
										lineItem, _ := item["lineItemRenderer"].(map[string]any)
										t.Author = youtubeText(lineItem["text"])
									}
								}
							}
						})
						if ad {
							return
						}
					}
					if len(t.ID) == 11 && t.Title != "" && !seen[t.ID] {
						seen[t.ID] = true
						tracks = append(tracks, t)
					}
					return
				}
			}
			keys := make([]string, 0, len(o))
			for k := range o {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if strings.Contains(strings.ToLower(k), "adslot") || strings.Contains(strings.ToLower(k), "promoted") || k == "menu" {
					continue
				}
				visit(o[k])
			}
		}
	}
	visit(root)
	return tracks
}

func recommendationFilter(query string, tracks []Track) []Track {
	t := normText(query)
	category := ""
	for _, s := range []string{"стендап", "standup", "апвоут", "upvote"} {
		if strings.Contains(t, s) {
			category = s
			break
		}
	}
	if category == "" {
		return listeningTracks(tracks)
	}
	var out []Track
	for _, tr := range tracks {
		n := normText(tr.Title + " " + tr.Author)
		match := strings.Contains(n, category)
		if category == "стендап" || category == "standup" {
			match = strings.Contains(n, "стендап") || strings.Contains(n, "standup") || strings.Contains(n, "stand up")
			// У сольных концертов жанр часто отсутствует в названии: учитываем канал исполнителя.
			for _, performer := range []string{"комиссаренко", "алексей щербаков", "нурлан сабуров", "иван абрамов"} {
				if strings.Contains(normText(tr.Author), performer) {
					match = true
				}
			}
		}
		if category == "апвоут" || category == "upvote" {
			match = strings.Contains(n, "апвоут") || strings.Contains(n, "upvote")
		}
		if match {
			out = append(out, tr)
		}
	}
	return listeningTracks(out)
}

func (m *Music) accountSearch(ctx context.Context, request string) ([]Track, error) {
	if m.Account == nil {
		return nil, errors.New("для поиска с твоим входом войди в YouTube")
	}
	tok, err := m.Account.Token(ctx)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, errors.New("вход в YouTube отсутствует")
	}
	query := "подкаст разговорные истории"
	n := normText(request)
	if strings.Contains(n, "стендап") || strings.Contains(n, "standup") {
		query = "стендап полный концерт"
	} else if strings.Contains(n, "апвоут") || strings.Contains(n, "upvote") {
		query = "апвоут истории"
	} else if strings.Contains(n, "аудиокниг") {
		query = "аудиокнига рассказ"
	}
	const version = "7.20260114.12.00"
	body := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "TVHTML5", "clientVersion": version, "hl": "ru", "gl": "RU", "platform": "TV", "deviceMake": "Samsung", "deviceModel": "SmartTV"}}, "query": query}
	api := &Music{client: m.Account.client}
	out, err := api.post(ctx, "https://www.youtube.com/youtubei/v1/search?prettyPrint=false", body, ytTVAgent, "Authorization", "Bearer "+tok, "X-YouTube-Client-Name", "7", "X-YouTube-Client-Version", version)
	if err != nil {
		return nil, err
	}
	tracks := recommendationTracks(out["contents"])
	filtered := recommendationFilter(request, tracks)
	if strings.Contains(n, "стендап") || strings.Contains(n, "standup") {
		seen := map[string]bool{}
		for _, tr := range filtered {
			seen[tr.ID] = true
		}
		for _, tr := range tracks {
			if !seen[tr.ID] && listenScore(tr) >= 0 && strings.Contains(normText(tr.Title), "концерт") {
				filtered = append(filtered, tr)
				seen[tr.ID] = true
			}
		}
	}
	return filtered, nil
}

// Kept as a diagnostic CLI: reads the feed without starting audio or changing history.
func probeRecommendations() {
	path := "/data/opt/jarvisd/settings.json"
	if len(os.Args) > 2 {
		path = os.Args[2]
	}
	store, err := OpenStore(path)
	if err != nil {
		panic(err)
	}
	p := probeProxy(store)
	defer p.Close()
	if err := p.Apply(store.Get().Vless); err != nil {
		fmt.Println("VPN:", err)
		return
	}
	client := p.ClientFor("youtube", 25*time.Second)
	m := NewMusic(func() *http.Client { return client }, func() float64 { return 1 })
	m.Account = NewYTAccount(store, func() *http.Client { return client })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var tracks []Track
	if len(os.Args) > 3 && strings.HasPrefix(os.Args[3], "related:") {
		tracks, err = m.watchRecommendations(ctx, relatedProbeArg(os.Args[3]))
	} else if len(os.Args) > 3 {
		tracks, err = m.accountSearch(ctx, os.Args[3])
	} else {
		tracks, err = m.recommendations(ctx)
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	b, _ := json.Marshal(tracks)
	fmt.Println(string(b))
}
