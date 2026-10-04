package feed

import "sync"

// Notifier будит long-poll запросы фидов при любом изменении, которое может поменять
// содержимое: правка списка, состав фида, его токен или включённость.
//
// Событие одно на всех: проснувшийся запрос сам пересобирает свой фид (из кеша Builder это
// дёшево) и сравнивает ETag. Так не нужно знать, какие фиды затронуло изменение.
type Notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func NewNotifier() *Notifier {
	return &Notifier{ch: make(chan struct{})}
}

// Wait возвращает канал, который закроется при следующем Notify. Брать его нужно
// до сборки фида, иначе изменение между сборкой и ожиданием потеряется.
func (n *Notifier) Wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

// Notify будит всех, кто ждёт сейчас.
func (n *Notifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}
