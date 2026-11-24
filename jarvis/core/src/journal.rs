//! Журнал последних реплик (в памяти) и короткая история диалога для контекста следующего разговора.

use serde::Serialize;
use std::{
    collections::VecDeque,
    sync::Mutex,
    time::{SystemTime, UNIX_EPOCH},
};

#[derive(Clone, Debug, Default, Serialize)]
pub struct Turn {
    pub id: i64,
    pub ts: f64,
    pub text: String,
    pub answer: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub audio_s: f64,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub live: bool,
    pub route: String,
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

fn is_zero(v: &f64) -> bool {
    *v == 0.0
}

pub fn now_s() -> f64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis() as f64 / 1000.0).unwrap_or(0.0)
}

#[derive(Default)]
struct St {
    items: VecDeque<Turn>, // новые — в начале
    next: i64,
    history: VecDeque<Turn>,
}

#[derive(Default)]
pub struct Journal {
    st: Mutex<St>,
    /// Реплика закончилась (общий чат ChatGPT, память).
    pub on_remember: Mutex<Option<Box<dyn Fn(Turn) + Send + Sync>>>,
}

impl Journal {
    pub fn new_turn(&self, route: &str, model: &str) -> i64 {
        let mut st = self.st.lock().unwrap();
        st.next += 1;
        let id = st.next;
        st.items.push_front(Turn { id, ts: now_s(), route: route.into(), model: model.into(), ..Default::default() });
        st.items.truncate(100);
        id
    }

    pub fn update(&self, id: i64, f: impl FnOnce(&mut Turn)) {
        if id == 0 {
            return;
        }
        let mut st = self.st.lock().unwrap();
        if let Some(t) = st.items.iter_mut().find(|t| t.id == id) {
            f(t);
        }
    }

    pub fn get(&self, id: i64) -> Option<Turn> {
        self.st.lock().unwrap().items.iter().find(|t| t.id == id).cloned()
    }

    pub fn remember(&self, id: i64) {
        let done = {
            let mut st = self.st.lock().unwrap();
            let Some(t) = st.items.iter().find(|t| t.id == id).cloned() else { return };
            if t.text.is_empty() && t.answer.is_empty() {
                return;
            }
            st.history.push_back(t.clone());
            while st.history.len() > 12 {
                st.history.pop_front();
            }
            t
        };
        if let Some(h) = self.on_remember.lock().unwrap().as_ref() {
            h(done);
        }
    }

    /// Реплики за последние 30 минут.
    pub fn history(&self) -> Vec<Turn> {
        let now = now_s();
        self.st.lock().unwrap().history.iter().filter(|t| now - t.ts < 1800.0).cloned().collect()
    }

    pub fn items(&self, limit: usize) -> Vec<Turn> {
        self.st.lock().unwrap().items.iter().take(limit).cloned().collect()
    }

    /// Реплика, выполненная без разговора с моделью (стоп, команда Алисе, умный дом).
    pub fn note(&self, route: &str, text: &str, answer: &str) {
        let id = self.new_turn(route, "");
        self.update(id, |t| {
            t.text = text.into();
            t.answer = answer.into();
        });
        self.remember(id);
    }

    /// Ошибка без разговора (нет ключа и т. п.).
    pub fn fail(&self, msg: &str) {
        let id = self.new_turn("Джарвис", "");
        self.update(id, |t| t.error = msg.into());
        log::info!("{msg}");
    }
}
