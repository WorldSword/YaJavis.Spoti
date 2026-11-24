//! Голосовые просьбы к Spotify: «включи в Spotify Linkin Park», «включи диджея в спотифай».

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Request {
    /// найти и включить
    Search(String),
    Dj,
    Liked,
    Radio,
    /// про Spotify сказали, а что включить — нет
    Ask,
}

const VERBS: &[&str] = &["включи", "поставь", "запусти", "сыграй", "найди", "играй", "вруби", "давай"];
const PREPOSITIONS: &[&str] = &["в", "на", "через", "из", "с"];
const FILLERS: &[&str] = &["джарвис", "пожалуйста", "песню", "песня", "трек", "музыку", "мне"];
const DJ: &[&str] = &["dj", "диджей", "диджея", "ди джей", "ди джея"];
const LIKED: &[&str] = &[
    "любимые треки", "любимую музыку", "любимые песни", "мои любимые треки", "мои любимые песни", "мои лайки", "лайки",
    "понравившиеся", "понравившиеся треки", "понравившиеся песни", "мое любимое", "любимое",
];
const RADIO: &[&str] = &["радио", "радио по треку", "радио по этому треку", "радио по песне", "радио по этой песне", "похожие", "похожее"];

fn bare(token: &str) -> String {
    token
        .trim_matches(|c: char| !c.is_alphanumeric())
        .to_lowercase()
        .replace('ё', "е")
}

fn is_spotify(word: &str) -> bool {
    ["spotify", "спотиф", "спотик"].iter().any(|p| word.starts_with(p))
}

/// «включи в Spotify Linkin Park» → Search("Linkin Park"). Без слова Spotify и без глагола — не наш запрос.
pub fn request(text: &str) -> Option<Request> {
    let tokens: Vec<&str> = text.split_whitespace().collect();
    let words: Vec<String> = tokens.iter().map(|t| bare(t)).collect();
    let at = words.iter().position(|w| is_spotify(w))?;
    if !words.iter().any(|w| VERBS.contains(&w.as_str())) {
        return None;
    }
    // «поставь на паузу спотифай», «следующий трек в спотифай» — это управление, а не просьба включить
    if crate::music::command(text).is_some() {
        return None;
    }
    let mut query = Vec::new();
    for (i, (token, word)) in tokens.iter().zip(&words).enumerate() {
        let before_spotify = i + 1 == at && PREPOSITIONS.contains(&word.as_str());
        if i == at || before_spotify || word.is_empty() || VERBS.contains(&word.as_str()) || FILLERS.contains(&word.as_str()) {
            continue;
        }
        query.push(token.trim_matches(|c: char| ",.!?;:".contains(c)));
    }
    let query = query.join(" ");
    let key = bare(&query);
    Some(if key.is_empty() {
        Request::Ask
    } else if DJ.contains(&key.as_str()) {
        Request::Dj
    } else if LIKED.contains(&key.as_str()) {
        Request::Liked
    } else if RADIO.contains(&key.as_str()) {
        Request::Radio
    } else {
        Request::Search(query)
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn search(q: &str) -> Option<Request> {
        Some(Request::Search(q.into()))
    }

    #[test]
    fn plain_search_requests() {
        assert_eq!(request("включи в Spotify Linkin Park"), search("Linkin Park"));
        assert_eq!(request("Джарвис, поставь песню Numb в спотифае"), search("Numb"));
        assert_eq!(request("запусти через спотифай Кино Группа крови!"), search("Кино Группа крови"));
    }

    #[test]
    fn needs_title() {
        assert_eq!(request("включи спотифай"), Some(Request::Ask));
        assert_eq!(request("Джарвис включи в Spotify музыку"), Some(Request::Ask));
    }

    #[test]
    fn dj_liked_radio() {
        assert_eq!(request("включи диджея в спотифай"), Some(Request::Dj));
        assert_eq!(request("включи DJ в Spotify"), Some(Request::Dj));
        assert_eq!(request("включи мои любимые треки в Spotify"), Some(Request::Liked));
        assert_eq!(request("включи в спотифай лайки"), Some(Request::Liked));
        assert_eq!(request("включи радио по этому треку в спотифай"), Some(Request::Radio));
    }

    #[test]
    fn not_ours() {
        assert_eq!(request("включи Linkin Park"), None);
        assert_eq!(request("что такое спотифай"), None);
        assert_eq!(request("включи музыку на ютубе"), None);
        assert_eq!(request("поставь на паузу спотифай"), None);
        assert_eq!(request("включи Spotify на другой колонке сегодня"), search("на другой колонке сегодня"));
    }
}
