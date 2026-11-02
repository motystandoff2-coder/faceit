# ELITE FPL Bot — Standoff 2 Faceit

Discord бот для организации матчей в Standoff 2.

## Возможности

- `/setup` — создаёт все каналы и категории 1 в 1 как на скриншотах
- `/delete` — удаляет все каналы и роли бота
- `/register` — регистрация игрока (присваивает уникальный ID)
- `/profile` — карточка профиля со статистикой

### Система матчей
1. Игроки заходят в голосовой канал `Lobby N`
2. Когда набирается `MIN_LOBBY_PLAYERS` (по умолчанию 10), в `#ranked` этого лобби автоматически начинается распик
3. Капитаны по очереди банят карты (T → CT → T → CT...) пока не останется 1
4. После распика появляется карточка матча с кнопками
5. Создаются голосовые каналы T и CT для команд
6. Результат отправляется через канал `отправить-результаты`

## Переменные окружения (Render)

| Переменная | Описание |
|---|---|
| `DISCORD_TOKEN` | Токен бота |
| `MIN_LOBBY_PLAYERS` | Минимум игроков для старта (по умолчанию 10) |
| `DB_PATH` | Путь к папке с БД (по умолчанию `./data`) |

## Структура каналов после /setup

```
🔴 | PRO LEAGUE          → #ranked + Lobby 1
🔴 | PRO LEAGUE          → #ranked + Lobby 2
🔴 | PRO LEAGUE          → #ranked + Lobby 3
🔴 | PRO REMASTERED      → #ranked + Lobby 4
🔴 | PRO REMASTERED      → #ranked + Lobby 5
🟣 | DIVISION LEAGUE     → #ranked + Lobby 1
🟣 | DIVISION LEAGUE     → #ranked + Lobby 2
🟣 | DIVISION LEAGUE     → #ranked + Lobby 3
🟣 | DIVISION REMASTERED → #ranked + Lobby 4
🟣 | DIVISION REMASTERED → #ranked + Lobby 5
🟢 | PROSPECT LEAGUE     → #ranked + Lobby 1
🟢 | PROSPECT LEAGUE     → #ranked + Lobby 2
🟢 | PROSPECT LEAGUE     → #ranked + Lobby 3
🟢 | PROSPECT REMASTERED → #ranked + Lobby 4
🟢 | PROSPECT REMASTERED → #ranked + Lobby 5
⚪ | DEFAULT LEAGUE       → #ranked + Lobby 1
⚪ | DEFAULT LEAGUE       → #ranked + Lobby 2
⚪ | DEFAULT LEAGUE       → #ranked + Lobby 3
⚪ | DEFAULT REMASTERED   → #ranked + Lobby 4
⚪ | DEFAULT REMASTERED   → #ranked + Lobby 5
📍 | SEND RESULT'S
    📌│отправить-результаты
    📊│история-игр
    📁│приоритет-игр
    📁│обычный-рег
```

## Деплой на Render

1. Загрузите на GitHub
2. Создайте новый Background Worker на Render
3. Укажите Docker как Runtime
4. Добавьте переменные окружения
